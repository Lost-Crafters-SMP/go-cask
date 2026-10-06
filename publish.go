package cask

import (
	"fmt"
	"io/fs"
	"strings"
)

type publication struct {
	exists   bool
	consumed bool
}

// Native publishers accept generated single components only. The directory is
// pinned by opening "." through a confined os.Root; no absolute paths are used.
func publicationNames(temp, final string) error {
	for _, name := range []string{temp, final} {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00") {
			return fmt.Errorf("invalid publication basename: %w", fs.ErrInvalid)
		}
	}
	if temp == final {
		return fs.ErrInvalid
	}
	return nil
}
