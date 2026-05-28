package protocol

import (
	"bytes"
	"testing"
)

// DrumSettings is currently opaque — we don't model the
// individual fields of the 480-byte payload, just round-trip the
// raw bytes. These tests lock in that invariant so a future
// refactor (e.g. when the field layout is reverse-engineered)
// can't silently break the backup/restore primitive.

func TestDrumSettings_RoundTrip(t *testing.T) {
	// Build a payload with non-trivial content so a "returns
	// zeros" bug would be visible. Pattern doesn't matter — only
	// that decode + encode produces the same bytes.
	original := make([]byte, DRSPayloadSize)
	for i := range original {
		original[i] = byte((i*17 + 3) & 0x7F)
	}

	d, err := ParseDrumSettings(original)
	if err != nil {
		t.Fatalf("ParseDrumSettings: %v", err)
	}
	encoded, err := d.EncodePayload()
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	if !bytes.Equal(encoded, original) {
		t.Errorf("round-trip changed %d bytes", diffCount(original, encoded))
	}
}

func TestDrumSettings_ParseRejectsBadSize(t *testing.T) {
	if _, err := ParseDrumSettings(make([]byte, 479)); err == nil {
		t.Error("expected error on too-short payload")
	}
	if _, err := ParseDrumSettings(make([]byte, 481)); err == nil {
		t.Error("expected error on too-long payload")
	}
}

func TestDrumSettings_EncodeRejectsBadSize(t *testing.T) {
	d := &DrumSettings{Bytes: make([]byte, 100)} // bypass Parse's guard
	if _, err := d.EncodePayload(); err == nil {
		t.Error("expected error when Bytes is wrong size")
	}
}

func TestDrumSettings_DecodeIsACopy(t *testing.T) {
	// Mutating the original buffer after Parse must NOT affect the
	// DrumSettings struct's bytes — otherwise round-trip via a
	// retained payload would silently corrupt.
	original := make([]byte, DRSPayloadSize)
	for i := range original {
		original[i] = 0x42
	}
	d, err := ParseDrumSettings(original)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	original[0] = 0x00 // mutate caller's buffer
	if d.Bytes[0] != 0x42 {
		t.Errorf("ParseDrumSettings aliased the input buffer (byte 0 changed)")
	}
}

func diffCount(a, b []byte) int {
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}
