// Drum settings (DRS / func 7) — 8 hardware drum-input definitions
// stored as 8 × 60-byte blocks. The S950 manual describes per-input
// fields (trigger threshold, MIDI note, retrigger timing, etc.) but
// the byte-level layout isn't part of the public docs and we
// haven't reverse-engineered it. This module ships DRS as an opaque
// round-trip payload — enough to back up + restore the block, but
// not to edit individual fields. Future work could derive the
// layout from the S950 service manual.
//
// Wire layout: F0 47 <chan> 07 40 00 00 <480 data bytes> <checksum> F7

package protocol

import "fmt"

// DRSPayloadSize is the data byte count between the AKAI header and
// the trailing checksum + F7. From the S950 SysEx doc: 8 inputs ×
// 60 bytes each.
const DRSPayloadSize = 480

// DrumSettings carries the raw DRS payload. It's intentionally
// opaque — Bytes is the 480 wire bytes the device sent, suitable
// for round-tripping back to SetDrum without modification.
//
// Treat this as a backup/restore primitive until we have a
// documented byte layout: a user can pull DRS from the device, save
// it, and push it back later to recover the drum-input setup
// without losing data.
type DrumSettings struct {
	Bytes []byte
}

// ParseDrumSettings validates the size of an inbound DRS payload
// and returns a DrumSettings carrying a copy of the bytes. The
// caller is responsible for checksum validation; ParseAkai does
// that upstream.
func ParseDrumSettings(payload []byte) (*DrumSettings, error) {
	if len(payload) != DRSPayloadSize {
		return nil, fmt.Errorf("DRS payload is %d bytes, want %d", len(payload), DRSPayloadSize)
	}
	out := make([]byte, DRSPayloadSize)
	copy(out, payload)
	return &DrumSettings{Bytes: out}, nil
}

// EncodePayload returns the 480 data bytes ready to wrap in an AKAI
// data envelope. Round-trip-safe: pass an unmodified DrumSettings
// from ParseDrumSettings straight through to BuildAkaiData and the
// resulting envelope is byte-identical to what the device sent.
func (d *DrumSettings) EncodePayload() ([]byte, error) {
	if len(d.Bytes) != DRSPayloadSize {
		return nil, fmt.Errorf("DrumSettings.Bytes is %d bytes, want %d", len(d.Bytes), DRSPayloadSize)
	}
	out := make([]byte, DRSPayloadSize)
	copy(out, d.Bytes)
	return out, nil
}
