package sysex

import (
	"math/rand"
	"testing"
)

// Known vectors from the Akai S900 SysEx format doc.
func TestEncodeSWKnownVectors(t *testing.T) {
	tests := []struct {
		name       string
		in         uint16
		wantB0, b1 byte
	}{
		// Doc: "zero is sent as 40 00H" — i.e., silence in offset-binary is 0x800.
		{"silence 0x800", 0x800, 0x40, 0x00},
		// Doc gives the max value example.
		{"max 4095", 4095, 0x7F, 0x7C},
		{"zero", 0, 0x00, 0x00},
		{"one", 1, 0x00, 0x04},
		{"0x020", 0x020, 0x01, 0x00},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b0, b1 := EncodeSW(tt.in)
			if b0 != tt.wantB0 || b1 != tt.b1 {
				t.Fatalf("EncodeSW(0x%03X) = 0x%02X 0x%02X, want 0x%02X 0x%02X",
					tt.in, b0, b1, tt.wantB0, tt.b1)
			}
			if got := DecodeSW(b0, b1); got != tt.in {
				t.Fatalf("DecodeSW round trip got 0x%03X, want 0x%03X", got, tt.in)
			}
		})
	}
}

func TestSWRoundTripExhaustive(t *testing.T) {
	for v := uint16(0); v < 4096; v++ {
		b0, b1 := EncodeSW(v)
		if b0 > 0x7F || b1 > 0x7F {
			t.Fatalf("EncodeSW(%d) produced non-7-bit byte: 0x%02X 0x%02X", v, b0, b1)
		}
		if got := DecodeSW(b0, b1); got != v {
			t.Fatalf("SW round-trip failed: %d -> 0x%02X 0x%02X -> %d", v, b0, b1, got)
		}
	}
}

func TestDBRoundTrip(t *testing.T) {
	for v := 0; v < 256; v++ {
		lo, hi := EncodeDB(byte(v))
		if lo > 0x7F || hi > 0x7F {
			t.Fatalf("EncodeDB(%d) produced non-7-bit byte: 0x%02X 0x%02X", v, lo, hi)
		}
		if got := DecodeDB(lo, hi); got != byte(v) {
			t.Fatalf("DB round-trip failed: %d -> 0x%02X 0x%02X -> %d", v, lo, hi, got)
		}
	}
}

func TestDBKnownVectors(t *testing.T) {
	tests := []struct{ in, lo, hi byte }{
		{0x00, 0x00, 0x00},
		{0x7F, 0x7F, 0x00},
		{0x80, 0x00, 0x01},
		{0xFF, 0x7F, 0x01},
		{0x42, 0x42, 0x00},
		{0xC3, 0x43, 0x01},
	}
	for _, tt := range tests {
		lo, hi := EncodeDB(tt.in)
		if lo != tt.lo || hi != tt.hi {
			t.Errorf("EncodeDB(0x%02X) = 0x%02X 0x%02X, want 0x%02X 0x%02X",
				tt.in, lo, hi, tt.lo, tt.hi)
		}
	}
}

func TestDWRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 1000; i++ {
		v := uint16(r.Uint32())
		b := EncodeDW(v)
		for j, x := range b {
			if x > 0x7F {
				t.Fatalf("EncodeDW(%d) byte %d non-7-bit: 0x%02X", v, j, x)
			}
		}
		if got := DecodeDW(b); got != v {
			t.Fatalf("DW round-trip: %d -> % X -> %d", v, b[:], got)
		}
	}
	// Edge cases.
	for _, v := range []uint16{0, 1, 0x7F, 0x80, 0xFF, 0x100, 0xFFFF} {
		if got := DecodeDW(EncodeDW(v)); got != v {
			t.Errorf("DW edge case %d failed", v)
		}
	}
}

func TestDDRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 1000; i++ {
		v := r.Uint32()
		b := EncodeDD(v)
		for j, x := range b {
			if x > 0x7F {
				t.Fatalf("EncodeDD(%d) byte %d non-7-bit: 0x%02X", v, j, x)
			}
		}
		if got := DecodeDD(b); got != v {
			t.Fatalf("DD round-trip: %d -> % X -> %d", v, b[:], got)
		}
	}
	for _, v := range []uint32{0, 1, 0x7F, 0x80, 0xFF, 0x100, 0xFFFF, 0x10000, 0xFFFFFFFF} {
		if got := DecodeDD(EncodeDD(v)); got != v {
			t.Errorf("DD edge case %d failed", v)
		}
	}
}

func TestTBRoundTrip(t *testing.T) {
	// 21-bit range: 0..0x1FFFFF.
	r := rand.New(rand.NewSource(3))
	for i := 0; i < 5000; i++ {
		v := r.Uint32() & 0x1FFFFF
		b := EncodeTB(v)
		for j, x := range b {
			if x > 0x7F {
				t.Fatalf("EncodeTB(%d) byte %d non-7-bit: 0x%02X", v, j, x)
			}
		}
		if got := DecodeTB(b); got != v {
			t.Fatalf("TB round-trip: %d -> % X -> %d", v, b[:], got)
		}
	}
}

func TestTBKnownVectors(t *testing.T) {
	// Doc: total words up to 475020, period up to 500000. Verify a few.
	tests := []struct {
		in   uint32
		want [3]byte
	}{
		{0, [3]byte{0, 0, 0}},
		{1, [3]byte{1, 0, 0}},
		{127, [3]byte{0x7F, 0, 0}},
		{128, [3]byte{0, 1, 0}},
		{0x1FFFFF, [3]byte{0x7F, 0x7F, 0x7F}},
	}
	for _, tt := range tests {
		got := EncodeTB(tt.in)
		if got != tt.want {
			t.Errorf("EncodeTB(%d) = % X, want % X", tt.in, got, tt.want)
		}
	}
}

func TestNameRoundTrip(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"KICK", "KICK"},
		{"", ""},
		{"SNARE 1", "SNARE 1"},
		{"1234567890", "1234567890"}, // exact 10
		{"OVERLONG NAME", "OVERLONG N"}, // truncate
		{"TR    \x00\x00\x00\x00", "TR"}, // trim trailing NUL/space
	}
	for _, tt := range tests {
		got := DecodeName(EncodeName(tt.in))
		if got != tt.want {
			t.Errorf("Name(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNameAllBytesValid(t *testing.T) {
	b := EncodeName("HELLOWORLD")
	for i, x := range b {
		if x > 0x7F {
			t.Errorf("EncodeName byte %d non-7-bit: 0x%02X", i, x)
		}
	}
}

func TestXorChecksum(t *testing.T) {
	// Empty -> 0.
	if got := XorChecksum(nil); got != 0 {
		t.Errorf("empty XOR = %d, want 0", got)
	}
	// XOR is associative & involutive.
	data := []byte{0x01, 0x02, 0x03}
	if got := XorChecksum(data); got != 0x00 {
		t.Errorf("XOR(1,2,3) = %d, want 0", got)
	}
	// Single byte.
	if got := XorChecksum([]byte{0x42}); got != 0x42 {
		t.Errorf("XOR(0x42) = %d, want 0x42", got)
	}
	// Result must be 7-bit.
	if got := XorChecksum([]byte{0x7F, 0x7F, 0x7F}); got != 0x7F {
		t.Errorf("XOR triple-7F = %d, want 0x7F", got)
	}
}

func TestValidateMIDIBytes(t *testing.T) {
	if err := ValidateMIDIBytes([]byte{0x00, 0x7F, 0x42}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := ValidateMIDIBytes([]byte{0x00, 0x80}); err == nil {
		t.Error("expected error for 0x80")
	}
}
