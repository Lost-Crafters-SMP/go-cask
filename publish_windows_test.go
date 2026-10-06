package cask

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsPublicationErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		collision   bool
		unsupported bool
		mapped      error
	}{
		{"native-collision", windows.STATUS_OBJECT_NAME_COLLISION, true, false, nil},
		{"file-exists", windows.ERROR_FILE_EXISTS, true, false, nil},
		{"already-exists", windows.ERROR_ALREADY_EXISTS, true, false, nil},
		{"not-supported", windows.STATUS_NOT_SUPPORTED, false, true, windows.ERROR_NOT_SUPPORTED},
		{"invalid-function", windows.ERROR_INVALID_FUNCTION, false, true, windows.ERROR_INVALID_FUNCTION},
		{"access-denied", windows.STATUS_ACCESS_DENIED, false, false, windows.ERROR_ACCESS_DENIED},
		{"sharing", windows.STATUS_SHARING_VIOLATION, false, false, windows.ERROR_SHARING_VIOLATION},
		{"invalid-parameter", windows.STATUS_INVALID_PARAMETER, false, false, windows.ERROR_INVALID_PARAMETER},
		{"cross-volume", windows.STATUS_NOT_SAME_DEVICE, false, false, windows.ERROR_NOT_SAME_DEVICE},
		{"missing", windows.STATUS_OBJECT_NAME_NOT_FOUND, false, false, windows.ERROR_FILE_NOT_FOUND},
		{"disk-full", windows.STATUS_DISK_FULL, false, false, windows.ERROR_DISK_FULL},
		{"io-error", windows.ERROR_IO_DEVICE, false, false, windows.ERROR_IO_DEVICE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := windowsPublicationError(tc.err)
			if result.exists != tc.collision || result.consumed || errors.Is(err, ErrUnsupportedFilesystem) != tc.unsupported {
				t.Fatalf("classification: %+v %v", result, err)
			}
			if tc.collision {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, tc.err) || !errors.Is(err, tc.mapped) {
				t.Fatalf("lost error identity: %v", err)
			}
		})
	}
	result, err := windowsPublicationError(nil)
	if err != nil || !result.consumed || result.exists {
		t.Fatalf("success: %+v %v", result, err)
	}
}

func TestWindowsNativePublication(t *testing.T) {
	s, root := newTestStore(t, Options{})
	d := digestOf(t, SHA512, []byte("complete"))
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
	stage := func() string {
		t.Helper()
		f, name, err := createTemporary(shard)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := f.WriteString("complete")
		if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
			t.Fatal(err)
		}
		return name
	}

	first := stage()
	result, err := publishObject(dir, first, final)
	if err != nil || !result.consumed || result.exists {
		t.Fatalf("native rename: %+v %v", result, err)
	}
	if _, err := shard.Lstat(first); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging not consumed: %v", err)
	}
	second := stage()
	result, err = publishObject(dir, second, final)
	if err != nil || !result.exists || result.consumed {
		t.Fatalf("native collision: %+v %v", result, err)
	}
	if err := shard.Remove(second); err != nil {
		t.Fatalf("losing rename handle leaked: %v", err)
	}
	third := stage()
	path, err := windows.UTF16PtrFromString(filepath.Join(filepath.Dir(objectPath(root, d)), third))
	if err != nil {
		t.Fatal(err)
	}
	// This real open handle forbids deletion/rename by omitting delete sharing.
	blocker, err := windows.CreateFile(path, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	result, publishErr := publishObject(dir, third, "new-final")
	closeErr := windows.CloseHandle(blocker)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if result.exists || result.consumed || !errors.Is(publishErr, windows.ERROR_SHARING_VIOLATION) || errors.Is(publishErr, ErrUnsupportedFilesystem) {
		t.Fatalf("sharing violation: %+v %v", result, publishErr)
	}
	if _, err := shard.Lstat(third); err != nil {
		t.Fatal("failed rename lost staging", err)
	}
	if _, err := shard.Lstat("new-final"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failure published destination: %v", err)
	}
	if err := shard.Remove(third); err != nil {
		t.Fatalf("failed rename handle leaked: %v", err)
	}
	got, err := shard.ReadFile(final)
	if err != nil || string(got) != "complete" {
		t.Fatalf("winner changed: %q %v", got, err)
	}
}

func TestWindowsRenameStructure(t *testing.T) {
	var info fileRenameInformation
	if info.replaceIfExists != 0 {
		t.Fatal("replacement enabled")
	}
	rootOffset, lengthOffset, nameOffset := uintptr(4), uintptr(8), uintptr(12)
	if unsafe.Sizeof(windows.Handle(0)) == 8 {
		rootOffset, lengthOffset, nameOffset = 8, 16, 20
	}
	if unsafe.Offsetof(info.rootDirectory) != rootOffset || unsafe.Offsetof(info.fileNameLength) != lengthOffset || unsafe.Offsetof(info.fileName) != nameOffset {
		t.Fatal("native structure layout mismatch")
	}
}

func testLinkPublication(_ *os.File, _, _ string) (publication, error) {
	return publication{}, ErrUnsupportedFilesystem
}
