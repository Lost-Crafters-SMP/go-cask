//go:build !linux && !darwin && !windows

package cask

import "os"

func testLinkPublication(_ *os.File, _, _ string) (publication, error) {
	return publication{}, ErrUnsupportedFilesystem
}
