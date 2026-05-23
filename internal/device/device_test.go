package device

import (
	"testing"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/sample"
)

// TestDecodeDumpRoundTrip builds a fake S950 dump in memory (header + blocks +
// F7), then decodes it and verifies the audio comes back identical.
func TestDecodeDumpRoundTrip(t *testing.T) {
	// 250 random words (just over MinTotalWords).
	words := make([]uint16, 250)
	for i := range words {
		words[i] = uint16(i*7) & 0x0FFF
	}

	header := protocol.SampleDumpHeader{
		Num: 1, BitsPerWord: 12,
		PeriodNS:   45351,
		TotalWords: uint32(len(words)),
		LoopStart:  0, LoopEnd: uint32(len(words)) - 1,
		Mode: 0,
	}
	buf := header.EncodeHeader()

	nBlocks := protocol.NumBlocks(uint32(len(words)))
	for i := 0; i < nBlocks; i++ {
		var blk [protocol.WordsPerBlock]uint16
		for k := 0; k < protocol.WordsPerBlock; k++ {
			idx := i*protocol.WordsPerBlock + k
			if idx < len(words) {
				blk[k] = words[idx]
			} else {
				blk[k] = sample.SilenceWord
			}
		}
		buf = append(buf, protocol.EncodeBlock(i, blk)...)
	}
	buf = append(buf, protocol.EOX)

	dump, err := decodeDump(buf)
	if err != nil {
		t.Fatalf("decodeDump: %v", err)
	}
	if dump.Header != header {
		t.Errorf("header round-trip mismatch:\n got %+v\nwant %+v", dump.Header, header)
	}
	if len(dump.Words) != len(words) {
		t.Fatalf("words len %d, want %d", len(dump.Words), len(words))
	}
	for i := range words {
		if dump.Words[i] != words[i] {
			t.Fatalf("words[%d] = 0x%03X, want 0x%03X", i, dump.Words[i], words[i])
		}
	}
}

func TestDecodeDumpDetectsCorruption(t *testing.T) {
	words := make([]uint16, 250)
	header := protocol.SampleDumpHeader{
		Num: 0, BitsPerWord: 12, PeriodNS: 45351,
		TotalWords: uint32(len(words)), LoopStart: 0, LoopEnd: 1, Mode: 0,
	}
	buf := header.EncodeHeader()
	for i := 0; i < protocol.NumBlocks(uint32(len(words))); i++ {
		var blk [protocol.WordsPerBlock]uint16
		buf = append(buf, protocol.EncodeBlock(i, blk)...)
	}
	buf = append(buf, protocol.EOX)
	// Flip a data byte in block 1.
	buf[19+protocol.BlockSize+5] ^= 0x01
	if _, err := decodeDump(buf); err == nil {
		t.Error("expected checksum error on corrupted dump")
	}
}

// TestDecodeDumpExtraTailBlock exercises the hardware-verified behavior that
// the S950 emits one block more than ceil(total_words / 60). The decoder must
// trim padding via the header's TotalWords field.
func TestDecodeDumpExtraTailBlock(t *testing.T) {
	const total uint32 = 1800
	words := make([]uint16, total)
	for i := range words {
		words[i] = uint16(i) & 0x0FFF
	}
	header := protocol.SampleDumpHeader{
		Num: 0, BitsPerWord: 12, PeriodNS: 84940,
		TotalWords: total, LoopStart: 1755, LoopEnd: 1800, Mode: 0,
	}
	buf := header.EncodeHeader()
	// 30 data blocks + 1 padding block = 31 blocks (what the S950 actually sends).
	nWriteBlocks := protocol.NumBlocks(total) + 1
	for i := 0; i < nWriteBlocks; i++ {
		var blk [protocol.WordsPerBlock]uint16
		for k := 0; k < protocol.WordsPerBlock; k++ {
			idx := i*protocol.WordsPerBlock + k
			if idx < len(words) {
				blk[k] = words[idx]
			} else {
				blk[k] = sample.SilenceWord
			}
		}
		buf = append(buf, protocol.EncodeBlock(i, blk)...)
	}
	buf = append(buf, protocol.EOX)

	dump, err := decodeDump(buf)
	if err != nil {
		t.Fatalf("decodeDump: %v", err)
	}
	if uint32(len(dump.Words)) != total {
		t.Fatalf("words len %d, want %d", len(dump.Words), total)
	}
	for i := range words {
		if dump.Words[i] != words[i] {
			t.Fatalf("words[%d] = 0x%03X, want 0x%03X", i, dump.Words[i], words[i])
		}
	}
}

func TestDecodeDumpRejectsShortBody(t *testing.T) {
	header := protocol.SampleDumpHeader{
		Num: 0, BitsPerWord: 12, PeriodNS: 45351,
		TotalWords: 300, // expects 5 blocks
		LoopStart:  0, LoopEnd: 1, Mode: 0,
	}
	buf := header.EncodeHeader()
	// Only one block + EOX (wrong total).
	var blk [protocol.WordsPerBlock]uint16
	buf = append(buf, protocol.EncodeBlock(0, blk)...)
	buf = append(buf, protocol.EOX)
	if _, err := decodeDump(buf); err == nil {
		t.Error("expected size mismatch error")
	}
}
