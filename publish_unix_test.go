//go:build linux || darwin

package cask

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

func testLinkPublication(dir *os.File, temp, final string) (publication, error) {
	return publishUnix(dir, temp, final, func(int, string, string) error { return unix.ENOSYS }, unix.Linkat)
}

func TestUnixPublicationErrors(t *testing.T) {
	dir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	for _, first := range []error{unix.ENOSYS, unix.EOPNOTSUPP} {
		for _, second := range []error{nil, unix.EEXIST, unix.ENOSYS, unix.EOPNOTSUPP, unix.EPERM, unix.EACCES, unix.EIO, unix.EXDEV, unix.ENOSPC, unix.EDQUOT, unix.EINVAL} {
			result, err := publishUnix(dir, "stage", "final",
				func(int, string, string) error { return first },
				func(int, string, int, string, int) error { return second })
			if result.consumed || result.exists != errors.Is(second, unix.EEXIST) {
				t.Fatalf("fallback result: %+v", result)
			}
			if second == nil || errors.Is(second, unix.EEXIST) {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, second) || errors.Is(err, ErrUnsupportedFilesystem) != unsupportedUnix(second) {
				t.Fatalf("fallback %v -> %v: %v", first, second, err)
			}
		}
	}
	for _, cause := range []error{unix.EINVAL, unix.EPERM, unix.EACCES, unix.EIO, unix.EXDEV, unix.ENOSPC, unix.EDQUOT, unix.ENOENT} {
		_, err := publishUnix(dir, "stage", "final", func(int, string, string) error { return cause },
			func(int, string, int, string, int) error {
				t.Fatal("fallback on operational error", cause)
				return nil
			})
		if !errors.Is(err, cause) || errors.Is(err, ErrUnsupportedFilesystem) {
			t.Fatal(err)
		}
	}
}

func TestUnixFallbackAndMixedRace(t *testing.T) {
	s, root := newTestStore(t, Options{})
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	// Two stores, one native and one forced hard-link fallback.
	s2, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s2.Close() }()
	for i, store := range []*Store{s, s2} {
		store.publish = func(dir *os.File, temp, final string) (publication, error) {
			ready <- struct{}{}
			<-release
			if i == 0 {
				return publishObject(dir, temp, final)
			}
			return testLinkPublication(dir, temp, final)
		}
	}
	d := digestOf(t, SHA256, []byte("complete"))
	var wg sync.WaitGroup
	for _, store := range []*Store{s, s2} {
		wg.Go(func() {
			if _, err := store.Put(testContext, d, strings.NewReader("complete")); err != nil {
				t.Error(err)
			}
		})
	}
	<-ready
	<-ready
	close(release)
	wg.Wait()
	assertNoTemporary(t, root)
}

func TestUnixPostPublicationCleanupError(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	failure := errors.New("cleanup denied")
	s.publish = testLinkPublication
	s.remove = func(*os.Root, string) error { return failure }
	d := digestOf(t, SHA256, []byte("complete"))
	if _, err := s.Put(testContext, d, strings.NewReader("complete")); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := s.Verify(testContext, d); err != nil {
		t.Fatalf("final object incomplete: %v", err)
	}
}

func TestUnixNativePublication(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	d := digestOf(t, SHA256, []byte("complete"))
	shard, final, err := s.shard(d, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shard.Close() }()
	dir, err := shard.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	stage := func(data string) string {
		t.Helper()
		file, name, err := createTemporary(shard)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteString(data)
		if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
			t.Fatal(err)
		}
		return name
	}
	first := stage("complete")
	result, err := publishObject(dir, first, final)
	if err != nil || result.exists {
		t.Fatalf("publish: %+v %v", result, err)
	}
	if !result.consumed {
		if err := shard.Remove(first); err != nil {
			t.Fatal(err)
		}
		t.Skip("exclusive rename unavailable here; hard-link fallback succeeded")
	}
	if _, err := shard.Lstat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native rename did not consume staging: %v", err)
	}
	second := stage("different staging")
	result, err = publishObject(dir, second, final)
	if err != nil || !result.exists || result.consumed {
		t.Fatalf("collision: %+v %v", result, err)
	}
	winner, winnerErr := shard.ReadFile(final)
	loser, loserErr := shard.ReadFile(second)
	if err := errors.Join(winnerErr, loserErr); err != nil || string(winner) != "complete" || string(loser) != "different staging" {
		t.Fatalf("collision mutated bytes: %q %q %v", winner, loser, err)
	}
	if err := shard.Remove(second); err != nil {
		t.Fatal(err)
	}
}

func TestUnixUnreadableObject(t *testing.T) {
	s, root := newTestStore(t, Options{})
	d := digestOf(t, SHA256, []byte("complete"))
	if _, err := s.Put(testContext, d, strings.NewReader("complete")); err != nil {
		t.Fatal(err)
	}
	path := objectPath(root, d)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o600) }()
	if file, err := os.Open(path); err == nil {
		_ = file.Close()
		t.Skip("privileges bypass unreadable permissions")
	}
	if _, err := s.Verify(testContext, d); !errors.Is(err, os.ErrPermission) || errors.Is(err, ErrCorruptObject) {
		t.Fatal(err)
	}
}
