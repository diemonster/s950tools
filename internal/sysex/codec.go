// Package sysex implements the data-type encodings used by the Akai S900/S950
// MIDI System Exclusive protocol: B, DB, DW, DD, TB, SW and DB* (string).
//
// All values travel as 7-bit MIDI data bytes (0x00..0x7F). Multi-byte primitives
// are little-endian. Reference: "Akai S900 MIDI System Exclusive Data Format,
// V2.0" (applies to the S950).
package sysex

import "fmt"

// EncodeDB encodes an 8-bit value as two MIDI bytes: low 7 bits, then bit 7.
func EncodeDB(v byte) (lo, hi byte) {
	return v & 0x7F, (v >> 7) & 0x01
}

// DecodeDB reverses EncodeDB.
func DecodeDB(lo, hi byte) byte {
	return (lo & 0x7F) | ((hi & 0x01) << 7)
}

// EncodeDW encodes a 16-bit little-endian value as 4 MIDI bytes (two DB groups).
func EncodeDW(v uint16) [4]byte {
	l0, h0 := EncodeDB(byte(v))
	l1, h1 := EncodeDB(byte(v >> 8))
	return [4]byte{l0, h0, l1, h1}
}

// DecodeDW reverses EncodeDW.
func DecodeDW(b [4]byte) uint16 {
	lo := DecodeDB(b[0], b[1])
	hi := DecodeDB(b[2], b[3])
	return uint16(lo) | uint16(hi)<<8
}

// EncodeDD encodes a 32-bit little-endian value as 8 MIDI bytes (four DB groups).
func EncodeDD(v uint32) [8]byte {
	var out [8]byte
	for i := 0; i < 4; i++ {
		lo, hi := EncodeDB(byte(v >> (8 * uint(i))))
		out[2*i], out[2*i+1] = lo, hi
	}
	return out
}

// DecodeDD reverses EncodeDD.
func DecodeDD(b [8]byte) uint32 {
	var v uint32
	for i := 0; i < 4; i++ {
		v |= uint32(DecodeDB(b[2*i], b[2*i+1])) << (8 * uint(i))
	}
	return v
}

// EncodeTB encodes a 21-bit value as 3 MIDI bytes, little-endian 7-bit groups.
func EncodeTB(v uint32) [3]byte {
	return [3]byte{
		byte(v & 0x7F),
		byte((v >> 7) & 0x7F),
		byte((v >> 14) & 0x7F),
	}
}

// DecodeTB reverses EncodeTB.
func DecodeTB(b [3]byte) uint32 {
	return uint32(b[0]&0x7F) |
		uint32(b[1]&0x7F)<<7 |
		uint32(b[2]&0x7F)<<14
}

// EncodeSW encodes a 12-bit sample word as 2 MIDI bytes.
// Wire layout: byte0 = 0 d11..d5, byte1 = 0 d4..d0 0 0.
// The S950 uses offset-binary, so silence = 0x800 (encodes to 0x40, 0x00).
func EncodeSW(w uint16) (b0, b1 byte) {
	w &= 0x0FFF
	return byte((w >> 5) & 0x7F), byte((w << 2) & 0x7F)
}

// DecodeSW reverses EncodeSW.
func DecodeSW(b0, b1 byte) uint16 {
	return (uint16(b0&0x7F) << 5) | (uint16(b1&0x7F) >> 2)
}

// EncodeName encodes a 10-character ASCII name as 20 MIDI bytes (10 × DB).
// Names shorter than 10 chars are space-padded; longer names are truncated.
// Non-ASCII bytes are replaced with '?'.
func EncodeName(s string) [20]byte {
	var out [20]byte
	b := []byte(s)
	for i := 0; i < 10; i++ {
		var c byte = ' '
		if i < len(b) {
			c = b[i]
			if c > 0x7F {
				c = '?'
			}
		}
		lo, hi := EncodeDB(c)
		out[2*i], out[2*i+1] = lo, hi
	}
	return out
}

// DecodeName reverses EncodeName, trimming trailing spaces.
func DecodeName(b [20]byte) string {
	var out [10]byte
	for i := 0; i < 10; i++ {
		out[i] = DecodeDB(b[2*i], b[2*i+1])
	}
	// Trim trailing spaces and NULs.
	n := 10
	for n > 0 && (out[n-1] == ' ' || out[n-1] == 0) {
		n--
	}
	return string(out[:n])
}

// XorChecksum returns the XOR of all bytes, masked to 7 bits.
//
// Two uses on the wire:
//   - sample-data block checksum: XOR of the 120 data bytes only (the
//     block-number byte preceding them is NOT included).
//   - AKAI exclusive message checksum: XOR of every byte after the 7-byte
//     header (F0 47 chan func 40 num 00), excluding the checksum and F7.
func XorChecksum(data []byte) byte {
	var c byte
	for _, b := range data {
		c ^= b
	}
	return c & 0x7F
}

// ValidateMIDIBytes returns an error if any byte exceeds 0x7F. Useful as a
// safety net before handing a buffer to the transport.
func ValidateMIDIBytes(data []byte) error {
	for i, b := range data {
		if b > 0x7F {
			return fmt.Errorf("byte %d (0x%02X) exceeds 7-bit range", i, b)
		}
	}
	return nil
}
