// Package waveformcache persists per-sample 12-bit words audio across
// sessions so the GUI's waveform display can show the real signal for
// device-sourced samples we previously uploaded, instead of falling
// back to the synthetic squiggle the moment the app restarts.
//
// Cache keys identify a sample by (slot, name, totalWords). That's
// enough to survive the common workflows — a hardware time-stretch
// changes totalWords (so the cache misses cleanly), a hardware
// resample preserves totalWords but the audio diff is sub-LSB at the
// 12-bit width we display. A slot+name+length collision across
// different audio is rare enough that we don't try to detect it; the
// fallback is the same synthetic squiggle the device samples already
// rendered before Phase 1B existed.
//
// File layout (v2, current):
//   magic       8 bytes "S950WAVE"
//   version     uint32 LE (2)
//   wordCount   uint32 LE
//   rateHz      uint32 LE
//   words       wordCount × uint16 LE
//
// v1 (history) lacked the rateHz field — version-1 files are treated
// as cache misses on read so the audio is re-fetched with its real
// dump-header rate the next time the user refreshes. The cache is
// purely additive — losing the directory is never fatal.
package waveformcache

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Cache is a directory of waveform blobs keyed by (slot, name,
// totalWords). One file per sample. Safe to share across goroutines
// when the underlying filesystem is — we hold no in-memory index, so
// each Put/Get is an independent OS call.
type Cache struct {
	dir string
}

// Key identifies a single cached waveform. Sanitised when used as a
// file name so weird sample names (slashes, control bytes) can't
// escape the cache directory.
type Key struct {
	Slot       int
	Name       string
	TotalWords int
}

// Entry is a cached waveform plus its device-reported playback rate.
// The rate is the SDS dump header's PeriodNS field converted to Hz —
// authoritative for the audio bytes regardless of what the SPRM
// claimed. Without it the GUI re-attaches at the SPRM rate on a
// cache hit, and any divergence (e.g. a 48k SPRM over 40k bytes on
// a kit cut on the S950) plays back at the wrong pitch.
type Entry struct {
	Words        []uint16
	SampleRateHz uint32
}

const (
	magic        = "S950WAVE"
	currentVer   = uint32(2)
	headerBytes  = len(magic) + 4 + 4 + 4 // magic + version + wordCount + rateHz
	wordBytes    = 2
	maxWordCount = 1 << 22 // 4M words ≈ 8 MB — well above the 1.5M-word EXM005 ceiling
)

// ErrCorrupt signals a cache file that exists but can't be parsed.
// Callers should treat it like a miss (the GUI will re-attach from
// the next live capture / explicit Get).
var ErrCorrupt = errors.New("waveformcache: file is corrupt or wrong version")

// New constructs a Cache rooted at dir. The directory is created on
// first Put — calling New does not touch the filesystem so dropping
// in a Cache for a never-used path is cheap.
func New(dir string) *Cache {
	return &Cache{dir: dir}
}

// Path returns the absolute file path Key would map to. Exposed for
// tests and the future "open cache folder" affordance.
func (c *Cache) Path(k Key) string {
	return filepath.Join(c.dir, k.fileName())
}

// Put writes entry to the cache under k. Returns nil on success; an
// error if the directory can't be created or the write fails. Refuses
// pathologically-large blobs to keep a corrupt-on-disk file from
// being silently accepted by a future Get.
func (c *Cache) Put(k Key, entry Entry) error {
	if len(entry.Words) == 0 {
		return errors.New("waveformcache: refusing to cache zero-length audio")
	}
	if len(entry.Words) > maxWordCount {
		return fmt.Errorf("waveformcache: %d words exceeds cap (%d)", len(entry.Words), maxWordCount)
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return fmt.Errorf("waveformcache: mkdir %q: %w", c.dir, err)
	}
	// Write to a temp file then atomic-rename so a crash mid-write
	// can't leave a half-formed cache entry.
	tmp, err := os.CreateTemp(c.dir, ".tmp-*.bin")
	if err != nil {
		return fmt.Errorf("waveformcache: tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below this point fails.
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmp.Write([]byte(magic)); err != nil {
		return err
	}
	if err := binary.Write(tmp, binary.LittleEndian, currentVer); err != nil {
		return err
	}
	if err := binary.Write(tmp, binary.LittleEndian, uint32(len(entry.Words))); err != nil {
		return err
	}
	if err := binary.Write(tmp, binary.LittleEndian, entry.SampleRateHz); err != nil {
		return err
	}
	if err := binary.Write(tmp, binary.LittleEndian, entry.Words); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, c.Path(k)); err != nil {
		return fmt.Errorf("waveformcache: rename: %w", err)
	}
	return nil
}

// Get reads the cached entry for k. Returns os.ErrNotExist when the
// file is missing (the normal cache-miss case), ErrCorrupt when the
// file exists but can't be parsed (also returned for v1 files —
// callers should re-fetch so the rate is captured), or another error
// on I/O failure.
func (c *Cache) Get(k Key) (Entry, error) {
	f, err := os.Open(c.Path(k))
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()

	hdr := make([]byte, headerBytes)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return Entry{}, ErrCorrupt
	}
	if string(hdr[:len(magic)]) != magic {
		return Entry{}, ErrCorrupt
	}
	version := binary.LittleEndian.Uint32(hdr[len(magic):])
	if version != currentVer {
		return Entry{}, ErrCorrupt
	}
	wordCount := binary.LittleEndian.Uint32(hdr[len(magic)+4:])
	if wordCount == 0 || wordCount > maxWordCount {
		return Entry{}, ErrCorrupt
	}
	// Cross-check: claimed wordCount must agree with the key. A
	// mismatch suggests the file is for a different sample that
	// happened to land at this name — treat as corrupt so the caller
	// re-captures rather than displaying mis-sized audio.
	if int(wordCount) != k.TotalWords {
		return Entry{}, ErrCorrupt
	}
	rateHz := binary.LittleEndian.Uint32(hdr[len(magic)+8:])

	words := make([]uint16, wordCount)
	if err := binary.Read(f, binary.LittleEndian, words); err != nil {
		return Entry{}, ErrCorrupt
	}
	return Entry{Words: words, SampleRateHz: rateHz}, nil
}

// Delete drops the cache entry for k. Missing files are not errors —
// the post-condition is "no file for this key", which a miss already
// satisfies.
func (c *Cache) Delete(k Key) error {
	err := os.Remove(c.Path(k))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Clear removes the entire cache directory. Used by an explicit
// "Clear waveform cache" affordance (future UI). Missing root dir
// is not an error.
func (c *Cache) Clear() error {
	err := os.RemoveAll(c.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// fileName composes the on-disk file name for a key. Names are
// sanitised — anything that isn't [A-Za-z0-9._-] becomes "_" — so a
// pathological sample name can't break out of the cache directory.
// totalWords is part of the key (not the file's body alone) so a
// hardware time-stretch produces a different file rather than a
// length mismatch on read.
func (k Key) fileName() string {
	return fmt.Sprintf("%03d-%s-%d.bin", k.Slot, sanitizeName(k.Name), k.TotalWords)
}

func sanitizeName(s string) string {
	if s == "" {
		return "unnamed"
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "unnamed"
	}
	return b.String()
}
