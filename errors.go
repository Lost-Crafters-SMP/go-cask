package cask

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidDigest indicates malformed digest syntax or an invalid zero digest.
	ErrInvalidDigest = errors.New("invalid digest")
	// ErrUnsupportedAlgorithm indicates an algorithm outside the approved set.
	ErrUnsupportedAlgorithm = errors.New("unsupported digest algorithm")
	// ErrDigestMismatch indicates supplied bytes do not match the expected digest.
	ErrDigestMismatch = errors.New("digest mismatch")
	// ErrCorruptObject indicates a stored object's observed digest does not match its key.
	ErrCorruptObject = errors.New("corrupt object")
	// ErrTooLarge indicates ingestion exceeded MaxBlobSize or int64 size accounting.
	ErrTooLarge = errors.New("blob too large")
	// ErrUnsupportedFilesystem indicates no supported atomic no-clobber publisher.
	ErrUnsupportedFilesystem = errors.New("filesystem does not support atomic no-clobber publication")
)

// DigestMismatchError describes a supplied stream with an unexpected digest.
type DigestMismatchError struct {
	Expected Digest
	Actual   Digest
}

func (e *DigestMismatchError) Error() string {
	return fmt.Sprintf("%v: expected %s, actual %s", ErrDigestMismatch, e.Expected, e.Actual)
}

// Unwrap returns ErrDigestMismatch.
func (e *DigestMismatchError) Unwrap() error { return ErrDigestMismatch }

// CorruptionError describes stored bytes that do not match their requested key.
type CorruptionError struct {
	Expected Digest
	Actual   Digest
}

func (e *CorruptionError) Error() string {
	return fmt.Sprintf("%v: expected %s, actual %s", ErrCorruptObject, e.Expected, e.Actual)
}

// Unwrap returns ErrCorruptObject.
func (e *CorruptionError) Unwrap() error { return ErrCorruptObject }
