package cask

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testContext = context.Background()

func newTestStore(t testing.TB, options Options) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	s, err := New(root, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, root
}

func objectPath(root string, d Digest) string {
	h := strings.Split(d.String(), ":")[1]
	return filepath.Join(root, "objects", d.Algorithm().String(), h[:2], h[2:4], h[4:])
}

func assertNoTemporary(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Errorf("temporary file leaked: %s", path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected absent %s: %v", path, err)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, a := range []Algorithm{SHA256, SHA512} {
		for _, n := range []int{0, 7, 3*1024*1024 + 19} {
			t.Run(fmt.Sprintf("%s/%d", a, n), func(t *testing.T) {
				s, root := newTestStore(t, Options{})
				data := bytes.Repeat([]byte{'x'}, n)
				d := digestOf(t, a, data)
				want := Info{Digest: d, Size: int64(n)}
				for range 2 {
					info, err := s.Put(testContext, d, bytes.NewReader(data))
					if err != nil || info != want {
						t.Fatalf("put: %v, %v", info, err)
					}
				}
				info, err := s.Verify(testContext, d)
				if err != nil || info != want {
					t.Fatalf("verify: %v, %v", info, err)
				}
				r, info, err := s.Open(testContext, d)
				if err != nil || info != want {
					t.Fatalf("open: %v, %v", info, err)
				}
				got, readErr := io.ReadAll(r)
				if err := errors.Join(readErr, r.Close()); err != nil || !bytes.Equal(got, data) {
					t.Fatalf("read: %v", err)
				}
				stored, err := os.ReadFile(objectPath(root, d))
				if err != nil || !bytes.Equal(stored, data) {
					t.Fatalf("layout: %v", err)
				}
				assertNoTemporary(t, root)
			})
		}
	}
}

func TestMissingAndValidation(t *testing.T) {
	s, root := newTestStore(t, Options{})
	d := digestOf(t, SHA256, []byte("missing"))
	if _, err := s.Verify(testContext, d); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if _, _, err := s.Open(testContext, d); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := s.Put(testContext, Digest{}, strings.NewReader("x")); !errors.Is(err, ErrInvalidDigest) {
		t.Fatal(err)
	}
	if _, err := s.Verify(testContext, Digest{}); !errors.Is(err, ErrInvalidDigest) {
		t.Fatal(err)
	}
	if _, _, err := s.Open(testContext, Digest{}); !errors.Is(err, ErrInvalidDigest) {
		t.Fatal(err)
	}
	if _, err := New(root, Options{MaxBlobSize: -1}); err == nil {
		t.Fatal("negative limit accepted")
	}
	if _, err := New(filepath.Join(root, "missing"), Options{}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestCorruptionAndUnverifiedOpen(t *testing.T) {
	s, root := newTestStore(t, Options{})
	data := []byte("original")
	d := digestOf(t, SHA256, data)
	if _, err := s.Put(testContext, d, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	// Bad duplicate input must still be consumed and rejected.
	_, err := s.Put(testContext, d, strings.NewReader("wrong"))
	var mismatch *DigestMismatchError
	if !errors.As(err, &mismatch) || !errors.Is(err, ErrDigestMismatch) || mismatch.Expected != d {
		t.Fatalf("mismatch: %v", err)
	}
	bad := []byte("external corruption")
	if err := os.WriteFile(objectPath(root, d), bad, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, verify := range []func() (Info, error){
		func() (Info, error) { return s.Verify(testContext, d) },
		func() (Info, error) { return s.Put(testContext, d, bytes.NewReader(data)) },
	} {
		_, err := verify()
		var corrupt *CorruptionError
		if !errors.As(err, &corrupt) || !errors.Is(err, ErrCorruptObject) || corrupt.Expected != d || corrupt.Actual != digestOf(t, SHA256, bad) {
			t.Fatalf("corruption: %v", err)
		}
	}
	r, info, err := s.Open(testContext, d)
	if err != nil || info.Size != int64(len(bad)) {
		t.Fatalf("open corrupt: %v", err)
	}
	got, readErr := io.ReadAll(r)
	if err := errors.Join(readErr, r.Close()); err != nil || !bytes.Equal(got, bad) {
		t.Fatalf("unverified read: %v", err)
	}
	got, err = os.ReadFile(objectPath(root, d))
	if err != nil || !bytes.Equal(got, bad) {
		t.Fatal("corrupt object changed")
	}
	assertNoTemporary(t, root)
}

type errorReader struct {
	err error
}

func (r errorReader) Read(p []byte) (int, error) { return copy(p, "partial bytes"), r.err }

type canceledReader struct{ cancel context.CancelFunc }

func (r canceledReader) Read(p []byte) (int, error) {
	r.cancel()
	return copy(p, "bytes"), nil
}

func TestIngestionFailures(t *testing.T) {
	failure := errors.New("reader failure")
	for _, tc := range []struct {
		name string
		src  io.Reader
		want error
	}{
		{"mismatch", strings.NewReader("wrong"), ErrDigestMismatch},
		{"data-and-error", errorReader{failure}, failure},
		{"partial-then-error", io.MultiReader(strings.NewReader("first"), errorReader{failure}), failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, root := newTestStore(t, Options{})
			d := digestOf(t, SHA256, []byte("expected"))
			if _, err := s.Put(testContext, d, tc.src); !errors.Is(err, tc.want) {
				t.Fatalf("put: %v", err)
			}
			assertAbsent(t, objectPath(root, d))
			assertNoTemporary(t, root)
		})
	}
	// EOF accompanying data is a successful completion.
	s, _ := newTestStore(t, Options{})
	d := digestOf(t, SHA256, []byte("partial bytes"))
	if _, err := s.Put(testContext, d, errorReader{io.EOF}); err != nil {
		t.Fatal(err)
	}
}

func TestSizeLimit(t *testing.T) {
	for _, n := range []int{0, 9, 10, 11, 100000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			s, root := newTestStore(t, Options{MaxBlobSize: 10})
			data := bytes.Repeat([]byte{'s'}, n)
			d := digestOf(t, SHA256, data)
			_, err := s.Put(testContext, d, bytes.NewReader(data))
			if n <= 10 && err != nil {
				t.Fatal(err)
			}
			if n > 10 {
				if !errors.Is(err, ErrTooLarge) {
					t.Fatal(err)
				}
				assertAbsent(t, objectPath(root, d))
			}
			assertNoTemporary(t, root)
		})
	}
}

func TestContextAndClose(t *testing.T) {
	s, root := newTestStore(t, Options{})
	d := digestOf(t, SHA256, []byte("bytes"))
	ctx, cancel := context.WithCancel(testContext)
	if _, err := s.Put(ctx, d, canceledReader{cancel}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertNoTemporary(t, root)
	if _, err := s.Verify(ctx, d); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := s.Open(ctx, d); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Put(testContext, d, strings.NewReader("bytes")); err != nil {
		t.Fatal(err)
	}
	readCtx, cancelRead := context.WithCancel(testContext)
	r, _, err := s.Open(readCtx, d)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	cancelRead()
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r2, _, err := s.Open(testContext, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(r2)
	if err := errors.Join(readErr, r2.Close()); err != nil || string(data) != "bytes" {
		t.Fatalf("reader invalidated by store close: %v", err)
	}
	if _, err := s.Verify(testContext, d); !errors.Is(err, fs.ErrClosed) {
		t.Fatal(err)
	}
}

type faultyStage struct {
	*os.File
	writeErr error
	syncErr  error
	closeErr error
	short    bool
}

func (f *faultyStage) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.short {
		return f.File.Write(p[:len(p)/2])
	}
	return f.File.Write(p)
}

func (f *faultyStage) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

func (f *faultyStage) Close() error { return errors.Join(f.File.Close(), f.closeErr) }

func TestFaultsAndCleanup(t *testing.T) {
	failure := errors.New("injected failure")
	for _, tc := range []struct {
		name string
		wrap func(*os.File) stagingFile
		want error
	}{
		{"write", func(f *os.File) stagingFile { return &faultyStage{File: f, writeErr: failure} }, failure},
		{"short", func(f *os.File) stagingFile { return &faultyStage{File: f, short: true} }, io.ErrShortWrite},
		{"sync", func(f *os.File) stagingFile { return &faultyStage{File: f, syncErr: failure} }, failure},
		{"close", func(f *os.File) stagingFile { return &faultyStage{File: f, closeErr: failure} }, failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, root := newTestStore(t, Options{})
			s.wrapTemp = tc.wrap
			d := digestOf(t, SHA256, []byte("content"))
			if _, err := s.Put(testContext, d, strings.NewReader("content")); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			assertAbsent(t, objectPath(root, d))
			assertNoTemporary(t, root)
		})
	}
	t.Run("publication", func(t *testing.T) {
		s, root := newTestStore(t, Options{})
		s.publish = func(*os.File, string, string) (publication, error) { return publication{}, fs.ErrPermission }
		d := digestOf(t, SHA256, []byte("content"))
		if _, err := s.Put(testContext, d, strings.NewReader("content")); !errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrUnsupportedFilesystem) {
			t.Fatal(err)
		}
		assertAbsent(t, objectPath(root, d))
		assertNoTemporary(t, root)
	})
	t.Run("cleanup-preserves-primary", func(t *testing.T) {
		s, _ := newTestStore(t, Options{})
		s.remove = func(*os.Root, string) error { return failure }
		d := digestOf(t, SHA256, []byte("expected"))
		_, err := s.Put(testContext, d, strings.NewReader("wrong"))
		if !errors.Is(err, failure) || !errors.Is(err, ErrDigestMismatch) {
			t.Fatal(err)
		}
	})
}

func TestSinglePassAndCommitCancellation(t *testing.T) {
	s, root := newTestStore(t, Options{})
	d := digestOf(t, SHA256, []byte("content"))
	// Deliberately mutate staging in a private test hook. This documents that
	// Put does not promise an independent stored-byte hash before publication.
	s.publish = func(dir *os.File, temp, final string) (publication, error) {
		if err := os.WriteFile(filepath.Join(filepath.Dir(objectPath(root, d)), temp), []byte("changed"), 0o600); err != nil {
			return publication{}, err
		}
		return publishObject(dir, temp, final)
	}
	if _, err := s.Put(testContext, d, strings.NewReader("content")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(testContext, d); !errors.Is(err, ErrCorruptObject) {
		t.Fatal(err)
	}

	s2, _ := newTestStore(t, Options{})
	ctx, cancel := context.WithCancel(testContext)
	s2.publish = func(dir *os.File, temp, final string) (publication, error) {
		result, err := publishObject(dir, temp, final)
		cancel() // Cancellation raced after commit.
		return result, err
	}
	if _, err := s2.Put(ctx, d, strings.NewReader("content")); err != nil {
		t.Fatalf("post-commit cancellation: %v", err)
	}
}

func TestConcurrentPublication(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(fmt.Sprint(distinct), func(t *testing.T) {
			s, root := newTestStore(t, Options{})
			const writers = 8
			ready := make(chan struct{}, writers)
			release := make(chan struct{})
			s.publish = func(dir *os.File, temp, final string) (publication, error) {
				ready <- struct{}{}
				<-release
				return publishObject(dir, temp, final)
			}
			var wg sync.WaitGroup
			for i := range writers {
				wg.Go(func() {
					data := "common data"
					if distinct {
						data = fmt.Sprintf("data %d", i)
					}
					d := digestOf(t, SHA256, []byte(data))
					if _, err := s.Put(testContext, d, strings.NewReader(data)); err != nil {
						t.Error(err)
					}
					if _, err := s.Verify(testContext, d); err != nil {
						t.Error(err)
					}
				})
			}
			for range writers {
				<-ready
			}
			close(release)
			wg.Wait()
			assertNoTemporary(t, root)
		})
	}
}

func TestCollisionRetainsStaging(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	d := digestOf(t, SHA256, []byte("content"))
	if _, err := s.Put(testContext, d, strings.NewReader("content")); err != nil {
		t.Fatal(err)
	}
	s.publish = func(dir *os.File, temp, final string) (publication, error) {
		result, err := publishObject(dir, temp, final)
		if err != nil || !result.exists || result.consumed {
			t.Errorf("collision: %+v %v", result, err)
		}
		shard, _, openErr := s.shard(d, false)
		if openErr != nil {
			return result, openErr
		}
		_, statErr := shard.Lstat(temp)
		return result, errors.Join(err, statErr, shard.Close())
	}
	if _, err := s.Put(testContext, d, strings.NewReader("content")); err != nil {
		t.Fatal(err)
	}
}

func TestPathSafety(t *testing.T) {
	for _, kind := range []string{"object-symlink", "shard-symlink", "directory-object", "objects-symlink"} {
		t.Run(kind, func(t *testing.T) {
			s, root := newTestStore(t, Options{})
			d := digestOf(t, SHA256, []byte("content"))
			path := objectPath(root, d)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory-object":
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			case "object-symlink":
				outside := filepath.Join(t.TempDir(), "outside")
				if err := os.WriteFile(outside, []byte("content"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "shard-symlink", "objects-symlink":
				path = filepath.Dir(path)
				if kind == "objects-symlink" {
					path = filepath.Join(root, "objects")
				}
				// Test-created empty hierarchy only; no stored objects exist.
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			if _, _, err := s.Open(testContext, d); err == nil {
				t.Fatal("unsafe object accepted by Open")
			}
			if _, err := s.Verify(testContext, d); err == nil {
				t.Fatal("unsafe object accepted by Verify")
			}
			if _, err := s.Put(testContext, d, strings.NewReader("content")); err == nil {
				t.Fatal("unsafe object accepted by Put")
			}
		})
	}
	if err := publicationNames("../escape", "final"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatal(err)
	}
}

type cancelOnCloseStage struct {
	*os.File
	cancel context.CancelFunc
}

func (s *cancelOnCloseStage) Close() error {
	err := s.File.Close()
	s.cancel()
	return err
}

func TestCancellationImmediatelyBeforePublication(t *testing.T) {
	s, root := newTestStore(t, Options{})
	ctx, cancel := context.WithCancel(testContext)
	defer cancel()
	s.wrapTemp = func(f *os.File) stagingFile { return &cancelOnCloseStage{File: f, cancel: cancel} }
	s.publish = func(*os.File, string, string) (publication, error) {
		t.Fatal("publication attempted after cancellation")
		return publication{}, nil
	}
	d := digestOf(t, SHA256, []byte("complete"))
	if _, err := s.Put(ctx, d, strings.NewReader("complete")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	assertAbsent(t, objectPath(root, d))
	assertNoTemporary(t, root)
	deadline, end := context.WithDeadline(testContext, time.Now().Add(-time.Second))
	defer end()
	if _, err := s.Put(deadline, d, strings.NewReader("complete")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

type ownedReader struct {
	*strings.Reader
	closed bool
}

func (r *ownedReader) Close() error { r.closed = true; return nil }

func TestCallerReaderOwnership(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	r := &ownedReader{Reader: strings.NewReader("complete")}
	d := digestOf(t, SHA256, []byte("complete"))
	if _, err := s.Put(testContext, d, r); err != nil || r.closed {
		t.Fatalf("source ownership: closed=%v, err=%v", r.closed, err)
	}
}

type noProgressReader struct{}

func (noProgressReader) Read([]byte) (int, error) { return 0, nil }

type badCountReader struct{ count int }

func (r badCountReader) Read([]byte) (int, error) { return r.count, nil }

func TestStreamEdges(t *testing.T) {
	if _, err := stream(testContext, io.Discard, noProgressReader{}, 0); !errors.Is(err, io.ErrNoProgress) {
		t.Fatal(err)
	}
	for _, count := range []int{-1, 1000000} {
		if _, err := stream(testContext, io.Discard, badCountReader{count}, 0); err == nil {
			t.Fatal("invalid reader count accepted")
		}
	}
	if n, err := stream(testContext, io.Discard, strings.NewReader("bytes"), math.MaxInt64); err != nil || n != 5 {
		t.Fatalf("maximum limit: %d %v", n, err)
	}
	var written bytes.Buffer
	if _, err := stream(testContext, &written, strings.NewReader("too many bytes"), 4); !errors.Is(err, ErrTooLarge) || written.Len() > 4 {
		t.Fatalf("limit exceeded on disk: %d %v", written.Len(), err)
	}
}

func TestPostPublicationFailure(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	failure := errors.New("publication finalization failure")
	s.publish = func(dir *os.File, temp, final string) (publication, error) {
		result, err := publishObject(dir, temp, final)
		return result, errors.Join(err, failure)
	}
	d := digestOf(t, SHA256, []byte("complete"))
	if _, err := s.Put(testContext, d, strings.NewReader("complete")); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := s.Verify(testContext, d); err != nil {
		t.Fatalf("complete final object lost after finalization failure: %v", err)
	}
}

type barrierReader struct {
	src     io.Reader
	started bool
	ready   chan struct{}
	release <-chan struct{}
}

func (r *barrierReader) Read(p []byte) (int, error) {
	if r.started && r.ready != nil {
		close(r.ready) // The first chunk was written, but ingestion is unfinished.
		r.ready = nil
		<-r.release
	}
	r.started = true
	return r.src.Read(p)
}

func TestCompleteReaderVisibility(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	data := bytes.Repeat([]byte{'v'}, 1<<20)
	d := digestOf(t, SHA256, data)
	ready, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := s.Put(testContext, d, &barrierReader{src: bytes.NewReader(data), ready: ready, release: release})
		result <- err
	}()
	<-ready
	if _, _, err := s.Open(testContext, d); !errors.Is(err, fs.ErrNotExist) {
		close(release)
		<-result
		t.Fatalf("unfinished ingestion became visible: %v", err)
	}
	close(release)
	// Observe during publication. Every successful Open must return complete
	// bytes, regardless of whether it happened before or after the native call.
	for {
		r, _, err := s.Open(testContext, d)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			<-result
			t.Fatal(err)
		}
		if err == nil {
			got, readErr := io.ReadAll(r)
			if err := errors.Join(readErr, r.Close()); err != nil || !bytes.Equal(got, data) {
				<-result
				t.Fatalf("reader saw partial final bytes: %d %v", len(got), err)
			}
		}
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}

func TestGeneratedStreamAndAlgorithmIsolation(t *testing.T) {
	s, root := newTestStore(t, Options{})
	const size int64 = 8<<20 + 5
	for _, a := range []Algorithm{SHA256, SHA512} {
		h := a.newHash()
		if _, err := io.Copy(h, io.LimitReader(zeroReader{}, size)); err != nil {
			t.Fatal(err)
		}
		d, err := NewDigest(a, h.Sum(nil))
		if err != nil {
			t.Fatal(err)
		}
		info, err := s.Put(testContext, d, io.LimitReader(zeroReader{}, size))
		if err != nil || info != (Info{Digest: d, Size: size}) {
			t.Fatalf("generated ingest: %+v %v", info, err)
		}
		if verified, err := s.Verify(testContext, d); err != nil || verified != info {
			t.Fatalf("generated verify: %+v %v", verified, err)
		}
		if _, err := os.Stat(objectPath(root, d)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "objects"))
	if err != nil || len(entries) != 2 || entries[0].Name() != "sha256" || entries[1].Name() != "sha512" {
		t.Fatalf("algorithm isolation: %v %v", entries, err)
	}
	assertNoTemporary(t, root)
}
