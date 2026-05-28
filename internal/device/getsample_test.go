package device

import (
	"strings"
	"testing"

	"github.com/bivers/s950/internal/protocol"
)

// buildEnvelope constructs the exact wire form an S950 (or our own
// PutSampleOpenLoop) emits: 19-byte SDS header + N blocks + trailing
// F7. Used by every ParseSampleDumpEnvelope test to keep payload
// construction in one place.
func buildEnvelope(slot uint16, total uint32, padWord uint16, fillWord uint16) []byte {
	hdr := protocol.SampleDumpHeader{
		Num:         slot,
		BitsPerWord: 12,
		PeriodNS:    22676, // ~44.1 kHz
		TotalWords:  total,
		LoopStart:   total - 5,
		LoopEnd:     total - 1,
		Mode:        0,
	}
	envelope := append([]byte(nil), hdr.EncodeHeader()...)
	nBlocks := protocol.NumBlocks(total)
	for i := 0; i < nBlocks; i++ {
		var blk [protocol.WordsPerBlock]uint16
		for j := 0; j < protocol.WordsPerBlock; j++ {
			idx := uint32(i*protocol.WordsPerBlock + j)
			if idx < total {
				blk[j] = fillWord
			} else {
				blk[j] = padWord // sentinel — must be trimmed by parser
			}
		}
		envelope = append(envelope, protocol.EncodeBlock(i, blk)...)
	}
	envelope = append(envelope, protocol.EOX)
	return envelope
}

func TestParseSampleDumpEnvelope_HappyPath(t *testing.T) {
	const slot = byte(7)
	const total = uint32(150) // 3 blocks (60 + 60 + 30 + padding)

	envelope := buildEnvelope(uint16(slot), total, 0xABC, 0x800)
	hdr, words, err := ParseSampleDumpEnvelope(envelope, slot)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if hdr.Num != uint16(slot) {
		t.Errorf("slot = %d, want %d", hdr.Num, slot)
	}
	if hdr.TotalWords != total {
		t.Errorf("TotalWords = %d, want %d", hdr.TotalWords, total)
	}
	if uint32(len(words)) != total {
		t.Errorf("words len = %d, want %d (padding should be trimmed)", len(words), total)
	}
	// Every word in [0, total) should be the fill value; the padding
	// 0xABC bytes must have been trimmed off the end.
	for i, w := range words {
		if w != 0x800 {
			t.Errorf("word[%d] = %#x, want 0x800", i, w)
		}
	}
}

func TestParseSampleDumpEnvelope_RejectsWrongSlot(t *testing.T) {
	envelope := buildEnvelope(3, 200, 0x800, 0x800)
	_, _, err := ParseSampleDumpEnvelope(envelope, 5)
	if err == nil {
		t.Fatal("expected error for slot mismatch")
	}
	if !strings.Contains(err.Error(), "slot 3") {
		t.Errorf("error message should name the wrong slot: %v", err)
	}
}

func TestParseSampleDumpEnvelope_RejectsTooShort(t *testing.T) {
	_, _, err := ParseSampleDumpEnvelope([]byte{0xF0, 0x7E, 0x01, 0xF7}, 0)
	if err == nil {
		t.Fatal("expected error for too-short envelope")
	}
}

func TestParseSampleDumpEnvelope_RejectsBadPrefix(t *testing.T) {
	// Same overall length as a real envelope but wrong code byte —
	// looks like a different SysEx message.
	envelope := buildEnvelope(0, 200, 0x800, 0x800)
	envelope[2] = 0x06 // not CodeSD (0x01)
	_, _, err := ParseSampleDumpEnvelope(envelope, 0)
	if err == nil {
		t.Fatal("expected error for non-dump prefix")
	}
}

func TestParseSampleDumpEnvelope_RejectsMissingEOX(t *testing.T) {
	envelope := buildEnvelope(0, 200, 0x800, 0x800)
	envelope[len(envelope)-1] = 0x00 // strip the F7
	_, _, err := ParseSampleDumpEnvelope(envelope, 0)
	if err == nil {
		t.Fatal("expected error for missing F7")
	}
}

func TestParseSampleDumpEnvelope_RejectsTruncatedBody(t *testing.T) {
	envelope := buildEnvelope(0, 1000, 0x800, 0x800)
	// Remove 100 bytes from the middle of the body but keep the
	// closing F7 in place. Post the partial-block tolerance change
	// (parser walks block-by-block, accepts a short last block),
	// middle-truncation manifests as a checksum mismatch on the
	// block whose data got shifted — equally a hard error, but
	// the message wording changed from "size mismatch" to
	// "decode block N".
	truncated := append([]byte(nil), envelope[:200]...)
	truncated = append(truncated, envelope[300:]...)
	_, _, err := ParseSampleDumpEnvelope(truncated, 0)
	if err == nil {
		t.Fatal("expected error for truncated body")
	}
}

func TestParseSampleDumpEnvelope_TrimsPadding(t *testing.T) {
	// 61 words = 2 blocks, second block has 1 real word + 59 padding.
	// The parser must trim padding so the caller doesn't see 0xABC
	// silence sentinels at the tail.
	envelope := buildEnvelope(0, 61, 0xABC, 0x100)
	_, words, err := ParseSampleDumpEnvelope(envelope, 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(words) != 61 {
		t.Errorf("len = %d, want 61", len(words))
	}
	// Spot-check the last word isn't the padding sentinel.
	if words[60] != 0x100 {
		t.Errorf("trailing word = %#x, want 0x100 (padding should have been trimmed)", words[60])
	}
}

func TestParseSampleDumpEnvelope_DetectsBadBlockChecksum(t *testing.T) {
	envelope := buildEnvelope(0, 200, 0x800, 0x800)
	// Corrupt one byte inside block 0's data area (just past the
	// 19-byte header + 1-byte block-number prefix). DecodeBlock's
	// XOR checksum should catch it.
	envelope[19+1+10] ^= 0x01
	_, _, err := ParseSampleDumpEnvelope(envelope, 0)
	if err == nil {
		t.Fatal("expected checksum error")
	}
}

// ---------- Sample dump receive flow ----------
//
// The S950 v2.0 firmware (hardware-verified) streams a sample dump
// as ONE long SysEx envelope: F0 7E 01 <header> <block 0> ... <block N> F7.
// The device pauses between blocks waiting for our pre-emptive ACK.
// Each ACK is the 4-byte form `F0 7E 7F F7`; we pump one every
// AckPumpInterval until the final envelope lands. See
// device.GetSampleAudio for the orchestration; the closed-loop
// per-block parse path was tried in development but the device's
// MIDI flow doesn't surface individual block envelopes — they're
// all part of the single dump SysEx that gomidi/rtmidi only
// delivers after F7.
