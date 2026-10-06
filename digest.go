package cask

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

// Algorithm identifies an approved cryptographic digest algorithm.
type Algorithm uint8

const (
	// SHA256 identifies SHA-256.
	SHA256 Algorithm = 1
	// SHA512 identifies SHA-512.
	SHA512 Algorithm = 2
)

// String returns the canonical algorithm name, or a diagnostic for invalid values.
func (a Algorithm) String() string {
	switch a {
	case SHA256:
		return "sha256"
	case SHA512:
		return "sha512"
	default:
		return fmt.Sprintf("Algorithm(%d)", a)
	}
}

func (a Algorithm) size() int {
	switch a {
	case SHA256:
		return sha256.Size
	case SHA512:
		return sha512.Size
	default:
		return 0
	}
}

func (a Algorithm) newHash() hash.Hash {
	if a == SHA256 {
		return sha256.New()
	}
	return sha512.New() // Only called after digest validation.
}

// Digest is a comparable content key. Its zero value is invalid.
// Different algorithms define independent keys even for identical content.
type Digest struct {
	algorithm Algorithm
	sum       [sha512.Size]byte
}

// NewDigest copies an exact-length raw digest for an approved algorithm.
func NewDigest(algorithm Algorithm, sum []byte) (Digest, error) {
	if algorithm.size() == 0 {
		return Digest{}, fmt.Errorf("algorithm %d: %w", algorithm, ErrUnsupportedAlgorithm)
	}
	if len(sum) != algorithm.size() {
		return Digest{}, fmt.Errorf("digest length %d for %s: %w", len(sum), algorithm, ErrInvalidDigest)
	}
	d := Digest{algorithm: algorithm}
	copy(d.sum[:], sum)
	return d, nil
}

// ParseDigest parses an explicit lowercase algorithm and exact-length hex digest.
// Uppercase hexadecimal digits are accepted and normalized.
func ParseDigest(text string) (Digest, error) {
	name, value, ok := strings.Cut(text, ":")
	if !ok || strings.Contains(value, ":") || strings.TrimSpace(text) != text {
		return Digest{}, ErrInvalidDigest
	}
	var a Algorithm
	switch name {
	case "sha256":
		a = SHA256
	case "sha512":
		a = SHA512
	default:
		return Digest{}, fmt.Errorf("algorithm %q: %w", name, ErrUnsupportedAlgorithm)
	}
	if len(value) != a.size()*2 {
		return Digest{}, ErrInvalidDigest
	}
	sum, err := hex.DecodeString(value)
	if err != nil {
		return Digest{}, fmt.Errorf("digest hex: %w: %w", ErrInvalidDigest, err)
	}
	return NewDigest(a, sum)
}

func (d Digest) validate() error {
	if d.algorithm == 0 {
		return ErrInvalidDigest
	}
	if d.algorithm.size() == 0 {
		return ErrUnsupportedAlgorithm
	}
	return nil
}

// Algorithm returns the digest's algorithm.
func (d Digest) Algorithm() Algorithm { return d.algorithm }

// Bytes returns a copy of the raw digest, or nil for an invalid digest.
func (d Digest) Bytes() []byte {
	if d.validate() != nil {
		return nil
	}
	return append([]byte(nil), d.sum[:d.algorithm.size()]...)
}

// String returns canonical text, or an unparseable diagnostic for invalid values.
func (d Digest) String() string {
	if d.validate() != nil {
		return "<invalid digest>"
	}
	return d.algorithm.String() + ":" + hex.EncodeToString(d.sum[:d.algorithm.size()])
}

// MarshalText implements encoding.TextMarshaler. Invalid digests are rejected.
func (d Digest) MarshalText() ([]byte, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	return []byte(d.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler without changing d on error.
func (d *Digest) UnmarshalText(text []byte) error {
	parsed, err := ParseDigest(string(text))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}
