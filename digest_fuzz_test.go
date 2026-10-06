package cask

import (
	"encoding/hex"
	"strings"
	"testing"
)

// fuzzDigestInputs seeds the digest parser fuzzers with accepted forms and the
// malformed shapes that must be rejected without panicking or producing a
// usable key.
func fuzzDigestInputs(f *testing.F) []string {
	f.Helper()
	valid256 := digestOf(f, SHA256, []byte("fuzz seed")).String()
	valid512 := digestOf(f, SHA512, []byte("fuzz seed")).String()
	value256 := strings.SplitN(valid256, ":", 2)[1]
	return []string{
		// Accepted forms.
		valid256,
		valid512,
		"sha256:" + strings.ToUpper(value256),
		// Missing, extra, or misplaced separators.
		"",
		value256,
		":",
		"sha256",
		"sha256:",
		"sha256:a:b",
		"sha256::" + value256,
		// Malformed lengths and alphabets.
		"sha256:abcd",
		"sha256:" + value256 + "ff",
		"sha512:" + value256,
		"sha256:" + strings.Repeat("g", 64),
		"sha256:0x" + value256[2:],
		"sha256:0x" + value256[:62],
		// Unknown algorithms and casings.
		"md5:" + value256,
		"SHA256:" + value256,
		"sha256 :" + value256,
		// Whitespace and embedded control characters.
		" " + valid256,
		valid256 + " ",
		valid512 + "\n",
		"sha256:\t" + value256,
		"sha256:" + value256[:32] + "\x00" + value256[32:],
		// Traversal-looking values must never parse into a usable key.
		"sha256:../../outside",
		"..\\..\\" + valid256,
	}
}

func FuzzParseDigest(f *testing.F) {
	for _, seed := range fuzzDigestInputs(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		d, err := ParseDigest(text)
		if err != nil {
			// Rejected input must never yield a usable or half-parsed key.
			if d.validate() == nil || d.String() != "<invalid digest>" || d.Bytes() != nil {
				t.Fatalf("rejected input %q produced %s", text, d)
			}
			return
		}
		name, value, ok := strings.Cut(d.String(), ":")
		if !ok || strings.Contains(value, ":") {
			t.Fatalf("canonical form of %q lacks a single separator: %s", text, d)
		}
		if name != "sha256" && name != "sha512" {
			t.Fatalf("parsed %q into unapproved algorithm %q", text, name)
		}
		if value != strings.ToLower(value) || strings.TrimSpace(value) != value {
			t.Fatalf("canonical form is not normalized: %s", d)
		}
		if value != hex.EncodeToString(d.Bytes()) {
			t.Fatalf("canonical value does not match digest bytes: %s", d)
		}
		// Canonical output must itself parse to the identical comparable key.
		reparsed, err := ParseDigest(d.String())
		if err != nil || reparsed != d || reparsed.String() != d.String() {
			t.Fatalf("round trip failed for %q: %s, %v", text, reparsed, err)
		}
	})
}

func FuzzDigestUnmarshalText(f *testing.F) {
	for _, seed := range fuzzDigestInputs(f) {
		f.Add([]byte(seed))
	}
	valid := digestOf(f, SHA512, []byte("unchanged")).String()
	f.Add([]byte(valid + "\x00"))
	f.Add([]byte{0x00})
	f.Add([]byte("\x00" + valid))
	f.Fuzz(func(t *testing.T, text []byte) {
		sentinel := digestOf(t, SHA512, []byte("unchanged"))
		receiver := sentinel
		err := receiver.UnmarshalText(text)
		parsed, parseErr := ParseDigest(string(text))
		if parseErr != nil {
			if err == nil {
				t.Fatalf("UnmarshalText accepted input ParseDigest rejects: %q", text)
			}
			if receiver != sentinel {
				t.Fatalf("receiver changed on error for %q: %s", text, receiver)
			}
			return
		}
		if err != nil {
			t.Fatalf("UnmarshalText rejected valid input %q: %v", text, err)
		}
		if receiver != parsed {
			t.Fatalf("UnmarshalText stored %s for %q, expected %s", receiver, text, parsed)
		}
	})
}

// FuzzPublicationNames checks the pure publication-name boundary: any accepted
// pair must be distinct single path components free of separators.
func FuzzPublicationNames(f *testing.F) {
	f.Add(".tmp-"+strings.Repeat("0123456789abcdef", 2), strings.Repeat("c0ffee", 10))
	f.Add("", "")
	f.Add(".", ".")
	f.Add("..", "..")
	f.Add("../escape", "final")
	f.Add("final", "..\\escape")
	f.Add("temp", "temp")
	f.Add("a/b", "c")
	f.Add("a", "b\\c")
	f.Add("a", "b:c")
	f.Add("temp\x00", "final")
	f.Fuzz(func(t *testing.T, temp, final string) {
		if err := publicationNames(temp, final); err != nil {
			return
		}
		for _, name := range []string{temp, final} {
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00") {
				t.Fatalf("accepted unsafe publication name %q (temp=%q final=%q)", name, temp, final)
			}
		}
		if temp == final {
			t.Fatalf("accepted identical publication names %q", temp)
		}
	})
}
