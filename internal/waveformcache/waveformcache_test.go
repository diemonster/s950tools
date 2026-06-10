package waveformcache

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestCache returns a Cache rooted in t.TempDir() so each test runs
// against an isolated, auto-cleaned directory.
func newTestCache(t *testing.T) *Cache {
	t.Helper()
	return New(t.TempDir())
}

func TestPutGetRoundTrip(t *testing.T) {
	c := newTestCache(t)
	k := Key{Slot: 0, Name: "KICK", TotalWords: 4}
	words := []uint16{0x800, 0x123, 0x456, 0xFFF}

	if err := c.Put(k, Entry{Words: words, SampleRateHz: 40000}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := c.Get(k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Words) != len(words) {
		t.Fatalf("len = %d, want %d", len(got.Words), len(words))
	}
	for i := range words {
		if got.Words[i] != words[i] {
			t.Errorf("word[%d] = %#x, want %#x", i, got.Words[i], words[i])
		}
	}
	if got.SampleRateHz != 40000 {
		t.Errorf("SampleRateHz = %d, want 40000", got.SampleRateHz)
	}
}

func TestGetMissing(t *testing.T) {
	c := newTestCache(t)
	_, err := c.Get(Key{Slot: 99, Name: "NOPE", TotalWords: 100})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestPutRejectsEmpty(t *testing.T) {
	c := newTestCache(t)
	err := c.Put(Key{Slot: 0, Name: "X", TotalWords: 0}, Entry{Words: []uint16{}})
	if err == nil {
		t.Fatal("expected error for empty audio")
	}
}

func TestPutRejectsOversize(t *testing.T) {
	c := newTestCache(t)
	// Construct a slice longer than maxWordCount without actually
	// allocating that many words — just lie about the length via a
	// pre-sized append. Simpler: use a 1-element slice + claim it's
	// oversized via the key would NOT trigger this — Put uses len(words).
	// So allocate maxWordCount+1 with `make`. Small enough for a test.
	words := make([]uint16, maxWordCount+1)
	err := c.Put(Key{Slot: 0, Name: "X", TotalWords: len(words)}, Entry{Words: words})
	if err == nil {
		t.Fatal("expected oversize error")
	}
}

func TestGetCorruptMagic(t *testing.T) {
	c := newTestCache(t)
	k := Key{Slot: 1, Name: "BAD", TotalWords: 2}
	// Put a real entry, then overwrite the magic so the next Get
	// fails the header check.
	if err := c.Put(k, Entry{Words: []uint16{1, 2}, SampleRateHz: 26040}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Path(k), []byte("NOTAWAVE0000"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := c.Get(k)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got %v", err)
	}
}

func TestGetCorruptShortFile(t *testing.T) {
	c := newTestCache(t)
	k := Key{Slot: 1, Name: "SHORT", TotalWords: 2}
	// File too short to even hold the header.
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Path(k), []byte("S950"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := c.Get(k)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got %v", err)
	}
}

func TestGetCorruptKeyMismatch(t *testing.T) {
	// A file landed where the key would point, but its wordCount
	// header says 4 words while the lookup claims 2. That's a stale
	// cache from a renamed-then-restored sample at the same slot —
	// treat as corrupt and let the caller re-capture.
	c := newTestCache(t)
	k := Key{Slot: 0, Name: "MISMATCH", TotalWords: 4}
	if err := c.Put(k, Entry{Words: []uint16{1, 2, 3, 4}}); err != nil {
		t.Fatal(err)
	}
	// Look up under a key with a different TotalWords. The path
	// embeds TotalWords so this would normally just be a miss — to
	// exercise the body check we manually rename the file to the
	// "wrong" key's path.
	wrongKey := Key{Slot: 0, Name: "MISMATCH", TotalWords: 2}
	if err := os.Rename(c.Path(k), c.Path(wrongKey)); err != nil {
		t.Fatal(err)
	}
	_, err := c.Get(wrongKey)
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("expected ErrCorrupt for body/key length mismatch, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	c := newTestCache(t)
	k := Key{Slot: 0, Name: "GONE", TotalWords: 2}
	if err := c.Put(k, Entry{Words: []uint16{1, 2}, SampleRateHz: 26040}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(k); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(c.Path(k)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file still present after Delete: %v", err)
	}
	// Idempotent — second delete is a no-op.
	if err := c.Delete(k); err != nil {
		t.Errorf("second Delete should be a no-op, got %v", err)
	}
}

func TestClear(t *testing.T) {
	c := newTestCache(t)
	if err := c.Put(Key{Slot: 0, Name: "A", TotalWords: 2}, Entry{Words: []uint16{1, 2}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(Key{Slot: 1, Name: "B", TotalWords: 2}, Entry{Words: []uint16{3, 4}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(c.dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cache dir survived Clear: %v", err)
	}
	// Idempotent — clearing an absent dir is fine.
	if err := c.Clear(); err != nil {
		t.Errorf("second Clear should be a no-op, got %v", err)
	}
}

func TestFileNameSanitisation(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"KICK", "KICK"},
		{"R8 FUNK SN", "R8_FUNK_SN"},
		// `.` is allowed in names (it's a common file-extension
		// hint); `/` becomes `_`. So `../escape` collapses to
		// `.._escape` — no path separator, can't traverse.
		{"../escape", ".._escape"},
		{"name/with/slashes", "name_with_slashes"},
		{"", "unnamed"},
		{"…!", "__"},
		{"R8_MAPL_01", "R8_MAPL_01"},
	}
	for _, tc := range cases {
		k := Key{Slot: 7, Name: tc.name, TotalWords: 42}
		got := k.fileName()
		// Must contain the sanitised name and not contain a path separator.
		if strings.ContainsRune(got, filepath.Separator) {
			t.Errorf("%q produced filename with separator: %q", tc.name, got)
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%q → %q, expected to contain %q", tc.name, got, tc.want)
		}
	}
}

func TestPathRespectsRoot(t *testing.T) {
	// Even a pathological name with traversal sequences must keep
	// the produced Path inside the cache root.
	c := newTestCache(t)
	k := Key{Slot: 0, Name: "../../etc/passwd", TotalWords: 1}
	p := c.Path(k)
	// filepath.Clean normalises both the file name and the join.
	abs, _ := filepath.Abs(p)
	rootAbs, _ := filepath.Abs(c.dir)
	if !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		t.Errorf("path %q escaped root %q", abs, rootAbs)
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	// After a successful Put, the cache directory contains exactly
	// one file (the final blob) — no .tmp-* leftovers.
	c := newTestCache(t)
	if err := c.Put(Key{Slot: 0, Name: "X", TotalWords: 2}, Entry{Words: []uint16{1, 2}}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected 1 file in cache dir, got %d: %v", len(entries), names)
	}
}
