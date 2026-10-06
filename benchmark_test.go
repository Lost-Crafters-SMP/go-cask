package cask

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func generatedBlob(size int64, prefix string) io.Reader {
	return io.MultiReader(strings.NewReader(prefix), io.LimitReader(zeroReader{}, size-int64(len(prefix))))
}

func generatedDigest(b *testing.B, size int64, prefix string) Digest {
	b.Helper()
	h := SHA256.newHash()
	if _, err := io.Copy(h, generatedBlob(size, prefix)); err != nil {
		b.Fatal(err)
	}
	d, err := NewDigest(SHA256, h.Sum(nil))
	if err != nil {
		b.Fatal(err)
	}
	return d
}

func BenchmarkPutNew(b *testing.B) {
	for _, size := range []int64{1 << 20, 100 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s, root := newTestStore(b, Options{})
			b.ReportAllocs()
			b.SetBytes(size)
			b.ResetTimer()
			for i := range b.N {
				b.StopTimer()
				prefix := fmt.Sprint(i)
				d := generatedDigest(b, size, prefix)
				reader := generatedBlob(size, prefix)
				b.StartTimer()
				if _, err := s.Put(testContext, d, reader); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				// Benchmark-owned objects only, outside measured ingestion. No
				// deletion operation is exposed by the library.
				if err := os.Remove(objectPath(root, d)); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
		})
	}
}

func BenchmarkVerify(b *testing.B) {
	for _, size := range []int64{1 << 20, 100 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s, _ := newTestStore(b, Options{})
			d := generatedDigest(b, size, "blob")
			if _, err := s.Put(testContext, d, generatedBlob(size, "blob")); err != nil {
				b.Fatal(err)
			}
			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := s.Verify(testContext, d); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkPutExisting(b *testing.B) {
	s, _ := newTestStore(b, Options{})
	data := bytes.Repeat([]byte{'x'}, 1<<20)
	d := digestOf(b, SHA256, data)
	if _, err := s.Put(testContext, d, bytes.NewReader(data)); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.Put(testContext, d, bytes.NewReader(data)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOpen(b *testing.B) {
	s, _ := newTestStore(b, Options{})
	d := digestOf(b, SHA256, nil)
	if _, err := s.Put(testContext, d, bytes.NewReader(nil)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		r, _, err := s.Open(testContext, d)
		if err != nil {
			b.Fatal(err)
		}
		if err := r.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConcurrentDuplicatePut(b *testing.B) {
	s, _ := newTestStore(b, Options{})
	data := bytes.Repeat([]byte{'x'}, 1<<20)
	d := digestOf(b, SHA256, data)
	if _, err := s.Put(testContext, d, bytes.NewReader(data)); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := s.Put(testContext, d, bytes.NewReader(data)); err != nil {
				b.Error(err)
			}
		}
	})
}
