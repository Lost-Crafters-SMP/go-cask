package cask

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishObject(dir *os.File, temp, final string) (publication, error) {
	return publishUnix(dir, temp, final, func(fd int, source, destination string) error {
		return unix.Renameat2(fd, source, fd, destination, unix.RENAME_NOREPLACE)
	}, unix.Linkat)
}
