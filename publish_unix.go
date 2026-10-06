//go:build linux || darwin

package cask

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

type renameOperation func(int, string, string) error
type linkOperation func(int, string, int, string, int) error

func unsupportedUnix(err error) bool {
	// EINVAL remains ambiguous and operational. Fallback is only on definite
	// unavailability; EPERM may be seccomp, never inferred as unsupported.
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP)
}

func publishUnix(dir *os.File, temp, final string, rename renameOperation, link linkOperation) (publication, error) {
	if err := publicationNames(temp, final); err != nil {
		return publication{}, err
	}
	fd := int(dir.Fd())
	defer runtime.KeepAlive(dir)
	err := rename(fd, temp, final)
	if err == nil {
		return publication{consumed: true}, nil
	}
	if errors.Is(err, unix.EEXIST) {
		return publication{exists: true}, nil
	}
	if !unsupportedUnix(err) {
		return publication{}, err
	}
	linkErr := link(fd, temp, fd, final, 0)
	if linkErr == nil {
		return publication{}, nil
	}
	if errors.Is(linkErr, unix.EEXIST) {
		return publication{exists: true}, nil
	}
	if unsupportedUnix(linkErr) {
		return publication{}, fmt.Errorf("native rename and link unavailable: %w", errors.Join(ErrUnsupportedFilesystem, err, linkErr))
	}
	return publication{}, linkErr
}
