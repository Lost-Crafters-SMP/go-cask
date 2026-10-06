package cask

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
)

// Info contains intrinsic blob information. Open does not verify its digest.
type Info struct {
	Digest Digest
	Size   int64
}

// Options configures ingestion, not quotas or eviction.
type Options struct {
	// MaxBlobSize is an ingestion limit in bytes. Zero means unlimited.
	MaxBlobSize int64
}

// Store is a confined filesystem store. Methods may run concurrently, but Close
// must not race with other methods. Published objects are never opened for writing.
type Store struct {
	root    *os.Root
	maxSize int64
	closed  bool

	// Per-store fault seams, set only by package tests before any operations.
	wrapTemp func(*os.File) stagingFile
	publish  func(*os.File, string, string) (publication, error)
	remove   func(*os.Root, string) error
}

type stagingFile interface {
	io.Writer
	Sync() error
	Close() error
}

// New opens an existing trusted root directory and initializes the object layout.
func New(root string, options Options) (*Store, error) {
	if options.MaxBlobSize < 0 {
		return nil, fmt.Errorf("MaxBlobSize must be nonnegative")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open store root: %w", err)
	}
	objects, err := childDirectory(r, "objects", true)
	if err != nil {
		return nil, errors.Join(err, r.Close())
	}
	if err = objects.Close(); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	return &Store{root: r, maxSize: options.MaxBlobSize}, nil
}

func childDirectory(parent *os.Root, name string, create bool) (*os.Root, error) {
	if create {
		if err := parent.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create directory %s: %w", name, err)
		}
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("inspect directory %s: %w", name, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("directory %s is not a plain directory: %w", name, fs.ErrInvalid)
	}
	return parent.OpenRoot(name)
}

func (s *Store) shard(d Digest, create bool) (*os.Root, string, error) {
	if s.closed {
		return nil, "", fs.ErrClosed
	}
	h := hex.EncodeToString(d.sum[:d.algorithm.size()])
	parts := []string{"objects", d.algorithm.String(), h[:2], h[2:4]}
	parent := s.root
	for _, part := range parts {
		child, err := childDirectory(parent, part, create)
		if parent != s.root {
			err = errors.Join(err, parent.Close())
		}
		if err != nil {
			if child != nil {
				err = errors.Join(err, child.Close())
			}
			return nil, "", err
		}
		parent = child
	}
	return parent, h[4:], nil
}

func createTemporary(shard *os.Root) (*os.File, string, error) {
	for range 10 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := ".tmp-" + hex.EncodeToString(random[:])
		file, err := shard.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return file, name, err
	}
	return nil, "", fmt.Errorf("temporary name collisions: %w", fs.ErrExist)
}

// Put consumes src even for duplicates. New ingestion hashes once while writing,
// checks writes/sync/close, and relies on faithful successful filesystem writes.
// Colliding existing objects are independently verified. Put never closes src.
// An error after publication can leave a complete final object; retry is safe.
func (s *Store) Put(ctx context.Context, expected Digest, src io.Reader) (info Info, retErr error) {
	if err := expected.validate(); err != nil {
		return Info{}, err
	}
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	shard, final, err := s.shard(expected, true)
	if err != nil {
		return Info{}, fmt.Errorf("put shard: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, shard.Close()) }()
	temp, name, err := createTemporary(shard)
	if err != nil {
		return Info{}, fmt.Errorf("create staging: %w", err)
	}
	var staged stagingFile = temp
	if s.wrapTemp != nil {
		staged = s.wrapTemp(temp)
	}
	open, consumed := true, false
	defer func() {
		if open {
			retErr = errors.Join(retErr, staged.Close())
		}
		if !consumed {
			remove := s.remove
			if remove == nil {
				remove = (*os.Root).Remove
			}
			if err := remove(shard, name); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("remove staging: %w", err))
			}
		}
	}()
	h := expected.algorithm.newHash()
	size, err := stream(ctx, io.MultiWriter(staged, h), src, s.maxSize)
	if err != nil {
		return Info{}, fmt.Errorf("ingest: %w", err)
	}
	actual, err := NewDigest(expected.algorithm, h.Sum(nil))
	if err != nil {
		return Info{}, err
	}
	if actual != expected {
		return Info{}, &DigestMismatchError{Expected: expected, Actual: actual}
	}
	if err := staged.Sync(); err != nil {
		return Info{}, fmt.Errorf("sync staging: %w", err)
	}
	open = false
	if err := staged.Close(); err != nil {
		return Info{}, fmt.Errorf("close staging: %w", err)
	}
	dir, err := shard.Open(".")
	if err != nil {
		return Info{}, fmt.Errorf("pin publication directory: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, dir.Close()) }()
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	publish := s.publish
	if publish == nil {
		publish = publishObject
	}
	result, err := publish(dir, name, final)
	consumed = result.consumed
	if err != nil {
		return Info{}, fmt.Errorf("publish: %w", err)
	}
	if result.exists {
		return verifyInShard(ctx, shard, final, expected)
	}
	// Publication committed: cancellation alone must not change success to failure.
	return Info{Digest: expected, Size: size}, nil
}

// stream handles data-plus-error readers, bounded writes, short writes, and
// pathological no-progress readers without buffering a complete blob.
func stream(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	buffer := make([]byte, 32*1024)
	var size int64
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return size, err
		}
		readBuffer := buffer
		if limit > 0 && limit-size < int64(len(buffer)) {
			readBuffer = buffer[:int(limit-size)+1]
		}
		n, readErr := src.Read(readBuffer)
		if n < 0 || n > len(readBuffer) {
			return size, fmt.Errorf("reader returned invalid count %d", n)
		}
		if err := ctx.Err(); err != nil {
			return size, err
		}
		if n > 0 {
			emptyReads = 0
			if size > math.MaxInt64-int64(n) || (limit > 0 && int64(n) > limit-size) {
				return size, ErrTooLarge
			}
			written, err := dst.Write(readBuffer[:n])
			if err != nil {
				return size, err
			}
			if written != n {
				return size, io.ErrShortWrite
			}
			size += int64(n)
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return size, io.ErrNoProgress
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return size, nil
			}
			return size, readErr
		}
	}
}

func openRegular(shard *os.Root, name string) (*os.File, fs.FileInfo, error) {
	observed, err := shard.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !observed.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("object is not a regular file: %w", fs.ErrInvalid)
	}
	file, err := shard.Open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("opened object is not a regular file: %w", fs.ErrInvalid)
	}
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	return file, info, nil
}

// Open returns an unverified streaming reader. The caller must close it.
// Context is checked between reads; it cannot forcibly interrupt a blocked read.
func (s *Store) Open(ctx context.Context, digest Digest) (io.ReadCloser, Info, error) {
	if err := digest.validate(); err != nil {
		return nil, Info{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, Info{}, err
	}
	shard, name, err := s.shard(digest, false)
	if err != nil {
		return nil, Info{}, fmt.Errorf("open shard: %w", err)
	}
	file, observed, err := openRegular(shard, name)
	err = errors.Join(err, shard.Close())
	if err != nil {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		return nil, Info{}, fmt.Errorf("open object: %w", err)
	}
	return &contextReader{ctx: ctx, file: file}, Info{Digest: digest, Size: observed.Size()}, nil
}

type contextReader struct {
	ctx  context.Context
	file *os.File
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.file.Read(p)
}

func (r *contextReader) Close() error { return r.file.Close() }

// Verify hashes a complete stored object. It is not a permanent certificate, and
// Verify followed by Open is not an atomic verified read.
func (s *Store) Verify(ctx context.Context, digest Digest) (info Info, retErr error) {
	if err := digest.validate(); err != nil {
		return Info{}, err
	}
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	shard, name, err := s.shard(digest, false)
	if err != nil {
		return Info{}, fmt.Errorf("verify shard: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, shard.Close()) }()
	return verifyInShard(ctx, shard, name, digest)
}

func verifyInShard(ctx context.Context, shard *os.Root, name string, digest Digest) (info Info, retErr error) {
	file, _, err := openRegular(shard, name)
	if err != nil {
		return Info{}, fmt.Errorf("verify object: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, file.Close()) }()
	h := digest.algorithm.newHash()
	size, err := stream(ctx, h, file, 0)
	if err != nil {
		return Info{}, fmt.Errorf("hash object: %w", err)
	}
	actual, err := NewDigest(digest.algorithm, h.Sum(nil))
	if err != nil {
		return Info{}, err
	}
	if actual != digest {
		return Info{}, &CorruptionError{Expected: digest, Actual: actual}
	}
	return Info{Digest: digest, Size: size}, nil
}

// Close releases the store root, is idempotent, and leaves returned readers open.
// It must not race with store methods.
func (s *Store) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.root.Close()
}
