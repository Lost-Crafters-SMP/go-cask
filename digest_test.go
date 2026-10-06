package cask

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func digestOf(t testing.TB, a Algorithm, data []byte) Digest {
	t.Helper()
	h := a.newHash()
	if _, err := h.Write(data); err != nil {
		t.Fatal(err)
	}
	d, err := NewDigest(a, h.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDigest(t *testing.T) {
	for _, a := range []Algorithm{SHA256, SHA512} {
		t.Run(a.String(), func(t *testing.T) {
			d := digestOf(t, a, []byte("hello"))
			sum := d.Bytes()
			copyDigest, err := NewDigest(a, sum)
			if err != nil || copyDigest != d || d.Algorithm() != a {
				t.Fatalf("construction: %v, %v", copyDigest, err)
			}
			sum[0] ^= 255
			if copyDigest != d || bytes.Equal(sum, d.Bytes()) {
				t.Fatal("digest aliases caller bytes")
			}
			upper := a.String() + ":" + strings.ToUpper(hex.EncodeToString(d.Bytes()))
			parsed, err := ParseDigest(upper)
			if err != nil || parsed != d {
				t.Fatalf("parse uppercase hex: %v, %v", parsed, err)
			}
			text, err := d.MarshalText()
			if err != nil || string(text) != d.String() {
				t.Fatalf("marshal: %s, %v", text, err)
			}
			var receiver Digest
			if err := receiver.UnmarshalText(text); err != nil || receiver != d {
				t.Fatalf("unmarshal: %v", err)
			}
			if err := receiver.UnmarshalText([]byte("bad")); err == nil || receiver != d {
				t.Fatal("failed unmarshal modified receiver")
			}
		})
	}
	zero := Digest{}
	if _, err := zero.MarshalText(); !errors.Is(err, ErrInvalidDigest) {
		t.Fatalf("zero marshal: %v", err)
	}
	if _, err := ParseDigest(zero.String()); err == nil || zero.Bytes() != nil {
		t.Fatal("zero value looks valid")
	}
	for _, a := range []Algorithm{0, 3, 255} {
		if _, err := NewDigest(a, make([]byte, 32)); !errors.Is(err, ErrUnsupportedAlgorithm) {
			t.Fatalf("algorithm %d: %v", a, err)
		}
	}
	if _, err := NewDigest(SHA256, []byte{1}); !errors.Is(err, ErrInvalidDigest) {
		t.Fatalf("length: %v", err)
	}
}

func TestParseInvalid(t *testing.T) {
	for _, text := range []string{
		"", "abcd", "sha256:", "sha256:ab", "sha256:" + strings.Repeat("a", 65),
		"sha512:" + strings.Repeat("a", 127), "sha256:" + strings.Repeat("g", 64),
		"sha256:0x" + strings.Repeat("a", 62), "sha256:" + strings.Repeat("a", 63) + " ",
		" sha256:" + strings.Repeat("a", 64), "sha256:a:b", "sha256:../../outside",
		"sha256:" + strings.Repeat("a", 31) + "\n" + strings.Repeat("a", 32),
	} {
		t.Run(text, func(t *testing.T) {
			if _, err := ParseDigest(text); !errors.Is(err, ErrInvalidDigest) {
				t.Fatalf("expected invalid digest: %v", err)
			}
		})
	}
	for _, name := range []string{"SHA256", "Sha512", "md5", "sha1", "sha3", "../sha256"} {
		if _, err := ParseDigest(name + ":" + strings.Repeat("a", 64)); !errors.Is(err, ErrUnsupportedAlgorithm) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
