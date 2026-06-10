// Package akaidisk reads and writes Akai S900/S950 floppy disk
// images — the raw 800 KB (DS/DD) sector dumps that a Gotek running
// FlashFloppy mounts in place of a physical floppy. Exporting a
// bootable image lets a kit built in the GUI load at power-on with
// no wire transfer, and (because the image carries an OVERALL
// SETTINGS file) lets the S950 wake up with controller-select
// already on RS-232C.
//
// Format knowledge derives from the akaiutil project's documentation
// of the on-disk structures (format facts only — no code is ported;
// akaiutil's license is restrictive) cross-checked against the
// S950's SysEx encodings: every disk file payload is byte-identical
// to the corresponding SysEx data payload after DB-decoding (each
// RAM byte travels as two 7-bit wire bytes). A program's disk file
// is the de-DB'd PRGM payload, a sample header is the de-DB'd SPRM,
// the overall-settings file is the de-DB'd OVS block.
//
// Layout (low-density, 819 200 bytes):
//
//	block size 1024; 800 blocks total
//	blocks 0..3   header:
//	  0x0000  64 × 24-byte directory entries
//	  0x0600  800 × 2-byte little-endian FAT entries
//	  0x0C40  64-byte volume label (S900/S950: all zero)
//	  0x0C80  padding to 0x1000
//	blocks 4..799 file data, FAT-chained, end-of-file code 0x8000
//
// The FAT marks the header blocks with 0x0000 — the same code as
// "free" (a documented quirk of the S900 floppy format) — so
// readers must never follow a chain into blocks 0..3 and writers
// simply never allocate them.
package akaidisk

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// BlockSize is the floppy filesystem block (and physical
	// sector) size in bytes.
	BlockSize = 1024
	// TotalBlocksLD is the block count of a low-density (800 KB)
	// image — the S950's native DS/DD format and what Translator/
	// FlashFloppy setups conventionally use.
	TotalBlocksLD = 0x320
	// ImageSizeLD is the byte size of a low-density image.
	ImageSizeLD = TotalBlocksLD * BlockSize // 819200
	// HeaderBlocks is the number of blocks the header (directory +
	// FAT + label) occupies on a low-density disk.
	HeaderBlocks = 4
	// DirEntries is the number of directory slots on a floppy.
	DirEntries = 64
	// dirEntrySize is the on-disk size of one directory entry.
	dirEntrySize = 24
	// fatOffset is the byte offset of the FAT within the image.
	fatOffset = DirEntries * dirEntrySize // 0x600
	// fatEnd is the FAT chain terminator for S900-family files.
	fatEnd = 0x8000
	// NameLen is the S900/S950 file name length (chars).
	NameLen = 10
)

// File types used by the S950. The directory's type byte is the
// uppercase ASCII letter; 0x00 marks a free slot.
const (
	TypeSample  = 'S' // 60-byte sample header + 12-bit packed data
	TypeProgram = 'P' // 38-byte program header + 70-byte keygroups
	TypeOverall = 'O' // 40-byte overall settings
	TypeDrum    = 'D' // drum (ME-35T) settings
	TypeFree    = 0x00
)

// OverallFileName is the conventional directory name of the
// overall-settings file ("OVERALL SE", 10 chars).
const OverallFileName = "OVERALL SE"

var (
	ErrBadSize   = errors.New("akaidisk: not a low-density S900/S950 image (size != 819200)")
	ErrDirFull   = errors.New("akaidisk: volume directory full (64 entries)")
	ErrDiskFull  = errors.New("akaidisk: image out of free blocks")
	ErrBadChain  = errors.New("akaidisk: corrupt FAT chain")
	ErrNameEmpty = errors.New("akaidisk: file name must not be empty")
)

// Entry is one directory entry.
type Entry struct {
	// Name is the file name, trimmed of padding (max 10 chars,
	// restricted charset: 0-9 A-Z a-z space # + - .).
	Name string
	// Type is the file type byte (TypeSample, TypeProgram, …).
	Type byte
	// Size is the file size in bytes (24-bit on disk).
	Size int
	// StartBlock is the first FAT block of the file's chain.
	StartBlock int
}

// SanitizeName maps a string into the S900 name charset, uppercased
// and truncated to NameLen. Characters outside the set become '.'
// (mirroring the sampler's own display fallback).
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if b.Len() >= NameLen {
			break
		}
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z',
			r == ' ', r == '#', r == '+', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('.')
		}
	}
	return b.String()
}

// DecodeDBPayload converts a DB-encoded SysEx data payload (two
// 7-bit wire bytes per RAM byte: low 7 bits then high bit) into raw
// RAM bytes — the form every S900 disk file stores. Odd-length
// input drops the trailing byte (defensive; well-formed payloads
// are even).
func DecodeDBPayload(wire []byte) []byte {
	out := make([]byte, len(wire)/2)
	for i := range out {
		out[i] = (wire[2*i] & 0x7F) | (wire[2*i+1] << 7)
	}
	return out
}

// PackSamples packs 12-bit offset-binary words (0x000..0xFFF,
// 0x800 = zero — the project's in-memory sample representation)
// into the S900 non-compressed disk sample format. The scheme
// splits N samples into two halves of P = ceil(N/2): the first 2P
// bytes hold, for each i < P, a shared-nibble byte followed by
// sample i's top 8 bits; the trailing P bytes hold the top 8 bits
// of samples P..2P-1. The shared byte's high nibble is sample i's
// bits 7..4 and its low nibble is sample P+i's bits 7..4 (of the
// 16-bit two's-complement representation). 3 bytes per 2 samples.
func PackSamples(words []uint16) []byte {
	n := len(words)
	if n == 0 {
		return nil
	}
	p := (n + 1) / 2
	// pcm16 returns the signed 16-bit representation of word i, or
	// silence for the pad position when n is odd.
	pcm16 := func(i int) uint16 {
		if i >= n {
			return 0
		}
		return uint16((int(words[i]&0x0FFF) - 2048) << 4)
	}
	out := make([]byte, 3*p)
	for i := 0; i < p; i++ {
		a := pcm16(i)
		b := pcm16(p + i)
		out[2*i] = byte(a&0x00F0) | byte((b&0x00F0)>>4)
		out[2*i+1] = byte(a >> 8)
		out[2*p+i] = byte(b >> 8)
	}
	return out
}

// UnpackSamples reverses PackSamples. n is the sample count (from
// the disk header's slen field — the packed data may carry one pad
// sample when n is odd).
func UnpackSamples(packed []byte, n int) ([]uint16, error) {
	p := (n + 1) / 2
	if len(packed) < 3*p {
		return nil, fmt.Errorf("akaidisk: packed data too short: %d bytes for %d samples", len(packed), n)
	}
	out := make([]uint16, n)
	for i := 0; i < p; i++ {
		hiA := uint16(packed[2*i+1]) << 8
		nibA := uint16(packed[2*i]&0xF0)
		a := int16(hiA | nibA)
		out[i] = uint16(int(a)>>4) + 2048
		if p+i < n {
			hiB := uint16(packed[2*p+i]) << 8
			nibB := uint16(packed[2*i]&0x0F) << 4
			b := int16(hiB | nibB)
			out[p+i] = uint16(int(b)>>4) + 2048
		}
	}
	return out, nil
}
