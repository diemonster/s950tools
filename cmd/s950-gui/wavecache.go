package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bivers/s950/internal/waveformcache"
)

// Phase 1B persistence: cache the host-side words12 buffer for each
// sample we've uploaded so the next session can re-attach real audio
// to device-sourced rows instead of falling back to the synthetic
// squiggle. The cache lives in the OS-standard user cache dir
// (~/Library/Caches/s950-tools on macOS, ~/.cache/s950-tools on
// Linux). Failures are non-fatal — a broken cache should never block
// the GUI from working with real device state.

const wavecacheSubdir = "s950-tools/waves"

// cacheDir returns the absolute directory the on-disk waveform cache
// uses. Centralised so app + tests agree without duplicating the OS
// cache-dir lookup.
func cacheDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("user cache dir: %w", err)
	}
	return filepath.Join(root, wavecacheSubdir), nil
}

// wavecache returns the App's lazily-initialised waveform cache. We
// don't fail loudly when the user cache dir isn't available — the
// caller will see a nil cache and skip persistence rather than crash
// the app on an unusual platform.
func (a *App) wavecache() *waveformcache.Cache {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache != nil {
		return a.cache
	}
	dir, err := cacheDir()
	if err != nil {
		// Log via the wire-log channel so it surfaces in dev
		// without spamming stderr. The cache becomes a no-op.
		return nil
	}
	a.cache = waveformcache.New(dir)
	return a.cache
}

// CachedWaveform is what GetCachedWaveform returns on a hit: the
// stored 12-bit words plus the playback rate the dump header
// reported when this entry was captured. Surfacing the rate lets the
// frontend re-attach at the device's actual rate instead of the
// possibly-stale SPRM rate.
type CachedWaveform struct {
	Words        []uint16 `json:"words"`
	SampleRateHz uint32   `json:"sampleRateHz"`
}

// GetCachedWaveform looks up host audio for a sample identified by
// (slot, name, totalWords) and returns the cached entry if any. A
// clean miss returns (nil, nil) — the frontend treats that the same
// as the cache being uninitialised. Errors are reserved for genuine
// I/O problems.
func (a *App) GetCachedWaveform(slot int, name string, totalWords int) (*CachedWaveform, error) {
	c := a.wavecache()
	if c == nil {
		return nil, nil
	}
	entry, err := c.Get(waveformcache.Key{Slot: slot, Name: name, TotalWords: totalWords})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, waveformcache.ErrCorrupt) {
			// Both are "no usable cache" — silent miss is the
			// right answer for the GUI. v1 files (no rate field)
			// also land here as ErrCorrupt, forcing a re-fetch so
			// the rate is captured before the next session.
			return nil, nil
		}
		return nil, err
	}
	return &CachedWaveform{
		Words:        entry.Words,
		SampleRateHz: entry.SampleRateHz,
	}, nil
}

// PutCachedWaveform persists the host audio for a (slot, name,
// totalWords) tuple along with the device-reported playback rate.
// Called by the Apply Slicing success path and after Copy from S950.
// Returns nil on success; non-fatal errors are returned so the
// frontend can decide whether to surface them (currently it just
// swallows them — a missing cache write degrades to the synthetic
// waveform on next launch, not a crash).
func (a *App) PutCachedWaveform(slot int, name string, totalWords int, words []uint16, sampleRateHz uint32) error {
	c := a.wavecache()
	if c == nil {
		return nil
	}
	return c.Put(
		waveformcache.Key{Slot: slot, Name: name, TotalWords: totalWords},
		waveformcache.Entry{Words: words, SampleRateHz: sampleRateHz},
	)
}

// ClearWaveformCache removes every persisted waveform. Wired to a
// future "Clear waveform cache" affordance in the UI; not user-
// reachable yet but exported so it's available without a follow-up
// Wails rebuild.
func (a *App) ClearWaveformCache() error {
	c := a.wavecache()
	if c == nil {
		return nil
	}
	return c.Clear()
}
