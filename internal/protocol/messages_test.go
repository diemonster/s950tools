package protocol

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/bivers/s950/internal/sysex"
)

func TestBuildAkaiRequest(t *testing.T) {
	got := BuildAkaiRequest(0, FuncRCAT, 0)
	want := []byte{0xF0, 0x47, 0x00, 0x03, 0x40, 0x00, 0x00, 0xF7}
	if !bytes.Equal(got, want) {
		t.Errorf("catalog request: got % X, want % X", got, want)
	}

	got = BuildAkaiRequest(5, FuncRSPRM, 7)
	want = []byte{0xF0, 0x47, 0x05, 0x04, 0x40, 0x07, 0x00, 0xF7}
	if !bytes.Equal(got, want) {
		t.Errorf("RSPRM #7 on ch5: got % X, want % X", got, want)
	}
}

func TestBuildAkaiDataAndParseAkai(t *testing.T) {
	payload := []byte{0x10, 0x20, 0x30, 0x40, 0x50}
	msg := BuildAkaiData(2, FuncSPRM, 9, payload)

	// Structure.
	if msg[0] != SOX || msg[len(msg)-1] != EOX {
		t.Fatalf("missing SOX/EOX")
	}
	if msg[1] != ManufacturerAKAI {
		t.Fatalf("wrong manufacturer ID")
	}
	if msg[2] != 2 || msg[3] != FuncSPRM || msg[4] != DeviceIDS950 || msg[5] != 9 || msg[6] != 0x00 {
		t.Fatalf("wrong header bytes: % X", msg[:7])
	}

	// Checksum.
	wantCksum := sysex.XorChecksum(payload)
	if msg[len(msg)-2] != wantCksum {
		t.Fatalf("checksum: got 0x%02X want 0x%02X", msg[len(msg)-2], wantCksum)
	}

	// Round trip.
	parsed, err := ParseAkai(msg)
	if err != nil {
		t.Fatalf("ParseAkai: %v", err)
	}
	if parsed.Channel != 2 || parsed.Function != FuncSPRM || parsed.Num != 9 {
		t.Errorf("decoded header wrong: %+v", parsed)
	}
	if !bytes.Equal(parsed.Payload, payload) {
		t.Errorf("payload mismatch: got % X want % X", parsed.Payload, payload)
	}

	// Tamper checksum.
	bad := append([]byte(nil), msg...)
	bad[len(bad)-2] ^= 0x01
	if _, err := ParseAkai(bad); err == nil {
		t.Error("expected checksum mismatch error")
	}
}

func TestParseAkaiBadInputs(t *testing.T) {
	cases := [][]byte{
		nil,
		{0xF0, 0xF7},                                  // too short
		{0xF0, 0x42, 0, 0, 0x40, 0, 0, 0, 0xF7},       // wrong manufacturer
		{0xF0, 0x47, 0, 0, 0x41, 0, 0, 0, 0xF7},       // wrong device id
		{0x00, 0x47, 0, 0, 0x40, 0, 0, 0, 0xF7},       // missing SOX
		{0xF0, 0x47, 0, 0, 0x40, 0, 0, 0, 0x00},       // missing EOX
	}
	for i, c := range cases {
		if _, err := ParseAkai(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestBuildRequestSampleDump(t *testing.T) {
	got := BuildRequestSampleDump(3)
	want := []byte{0xF0, 0x7E, 0x00, 0x03, 0x00, 0xF7}
	if !bytes.Equal(got, want) {
		t.Errorf("RSD: got % X, want % X", got, want)
	}
}

func TestHandshakeRoundTrip(t *testing.T) {
	for _, code := range []byte{CodeACKS, CodeNAKS, CodeASD} {
		msg := BuildHandshake(code)
		if len(msg) != 4 {
			t.Errorf("handshake len %d, want 4", len(msg))
		}
		ok, got := IsHandshake(msg)
		if !ok || got != code {
			t.Errorf("IsHandshake on % X = (%v,0x%02X), want (true,0x%02X)", msg, ok, got, code)
		}
	}
	// Negative cases.
	if ok, _ := IsHandshake(nil); ok {
		t.Error("nil should not be a handshake")
	}
	if ok, _ := IsHandshake([]byte{0xF0, 0x7E, 0x10, 0xF7}); ok {
		t.Error("unknown code should not be a handshake")
	}
	if ok, _ := IsHandshake([]byte{0xF0, 0x7F, 0x7F, 0xF7}); ok {
		t.Error("non-UniversalNRT id should not be a handshake")
	}
}

func TestParseCatalog(t *testing.T) {
	// Two entries: a program "KICK" #1 and a sample "SNARE" #2.
	pad := func(s string) []byte {
		b := []byte(s)
		for len(b) < 10 {
			b = append(b, ' ')
		}
		return b[:10]
	}
	payload := []byte{}
	payload = append(payload, 'P', 0x01)
	payload = append(payload, pad("KICK")...)
	payload = append(payload, 'S', 0x02)
	payload = append(payload, pad("SNARE")...)

	entries, err := ParseCatalog(payload)
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[0] != (CatalogEntry{Type: 'P', Num: 1, Name: "KICK"}) {
		t.Errorf("entry 0 = %+v", entries[0])
	}
	if entries[1] != (CatalogEntry{Type: 'S', Num: 2, Name: "SNARE"}) {
		t.Errorf("entry 1 = %+v", entries[1])
	}

	// Bad length.
	if _, err := ParseCatalog([]byte{0, 0, 0}); err == nil {
		t.Error("expected error on non-multiple-of-12 payload")
	}
}

func TestSampleParamsRoundTrip(t *testing.T) {
	// Build a synthetic 120-byte payload with all known fields set and some
	// random "reserved" bytes that must round-trip untouched.
	r := rand.New(rand.NewSource(42))
	var raw [120]byte
	for i := range raw {
		raw[i] = byte(r.Intn(128))
	}
	// Make sure the high-level slots have valid encodings so parse-then-encode
	// is a fixed point (we then mutate via the high-level fields).
	p := &SampleParams{
		Raw:          raw,
		Name:         "TEST",
		TotalWords:   12345,
		SampleRateHz: 22050,
		NominalPitch: 960, // C3
		LoudOffset:   -100,
		ReplayMode:   ReplayLoop,
		End:          12000,
		Start:        0,
		LoopLength:   500,
		VelXFade:     0,
		Reversed:     'N',
	}
	payload := p.EncodePayload()
	if len(payload) != 120 {
		t.Fatalf("payload len %d, want 120", len(payload))
	}
	// All bytes must be 7-bit.
	for i, b := range payload {
		if b > 0x7F {
			t.Errorf("payload byte %d non-7-bit: 0x%02X", i, b)
		}
	}

	q, err := ParseSampleParams(payload)
	if err != nil {
		t.Fatalf("ParseSampleParams: %v", err)
	}
	if q.Name != p.Name {
		t.Errorf("name: got %q want %q", q.Name, p.Name)
	}
	if q.TotalWords != p.TotalWords {
		t.Errorf("TotalWords: got %d want %d", q.TotalWords, p.TotalWords)
	}
	if q.SampleRateHz != p.SampleRateHz {
		t.Errorf("SampleRateHz: got %d want %d", q.SampleRateHz, p.SampleRateHz)
	}
	if q.NominalPitch != p.NominalPitch {
		t.Errorf("NominalPitch: got %d want %d", q.NominalPitch, p.NominalPitch)
	}
	if q.LoudOffset != p.LoudOffset {
		t.Errorf("LoudOffset: got %d want %d", q.LoudOffset, p.LoudOffset)
	}
	if q.ReplayMode != p.ReplayMode {
		t.Errorf("ReplayMode: got %c want %c", q.ReplayMode, p.ReplayMode)
	}
	if q.End != p.End || q.Start != p.Start || q.LoopLength != p.LoopLength {
		t.Errorf("loop fields wrong: %+v want %+v", q, p)
	}
	if q.Reversed != p.Reversed {
		t.Errorf("Reversed: got %c want %c", q.Reversed, p.Reversed)
	}

	// Re-encoding parsed params must reproduce identical bytes (fixed point).
	again := q.EncodePayload()
	if !bytes.Equal(again, payload) {
		t.Errorf("parse/encode not a fixed point")
	}
}

func TestSampleParamsPreservesReserved(t *testing.T) {
	// Encode a payload, mutate one "reserved" byte in Raw, re-encode, confirm
	// the reserved byte survives untouched.
	p := &SampleParams{
		Name: "X", TotalWords: 1000, SampleRateHz: 22050,
		NominalPitch: 960, ReplayMode: ReplayOneShot, Reversed: 'N',
		End: 1000,
	}
	out := p.EncodePayload()

	q, err := ParseSampleParams(out)
	if err != nil {
		t.Fatal(err)
	}
	// Pick a known "reserved" byte (offset 28 — inside the DD at 27..34).
	const reservedIdx = 28
	q.Raw[reservedIdx] = 0x42

	out2 := q.EncodePayload()
	if out2[reservedIdx] != 0x42 {
		t.Errorf("reserved byte not preserved: got 0x%02X", out2[reservedIdx])
	}
}

func TestSampleParamsRejectsBadLength(t *testing.T) {
	if _, err := ParseSampleParams(make([]byte, 100)); err == nil {
		t.Error("expected error on short payload")
	}
}

func TestSampleDumpHeaderRoundTrip(t *testing.T) {
	h := SampleDumpHeader{
		Num: 7, BitsPerWord: 12,
		PeriodNS: 45351, TotalWords: 22050,
		LoopStart: 21000, LoopEnd: 22049,
		Mode: 0,
	}
	buf := h.EncodeHeader()
	if len(buf) != 19 {
		t.Fatalf("header len %d, want 19", len(buf))
	}
	if buf[0] != SOX || buf[1] != UniversalNRT || buf[2] != CodeSD {
		t.Fatalf("header prefix wrong: % X", buf[:3])
	}
	for i, b := range buf {
		if b > 0x7F && i != 0 { // F0 is allowed at byte 0
			t.Errorf("header byte %d non-7-bit: 0x%02X", i, b)
		}
	}

	got, err := ParseHeader(buf)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if *got != h {
		t.Errorf("header round-trip mismatch:\n got %+v\nwant %+v", *got, h)
	}
}

func TestEncodeBlockChecksum(t *testing.T) {
	var words [WordsPerBlock]uint16
	for i := range words {
		words[i] = uint16(i * 17 & 0xFFF)
	}
	b := EncodeBlock(3, words)
	if len(b) != BlockSize {
		t.Fatalf("block size %d, want %d", len(b), BlockSize)
	}
	if b[0] != 3 {
		t.Errorf("block num %d, want 3", b[0])
	}
	// Checksum should be XOR of the 120 data bytes (NOT the block number).
	wantCksum := sysex.XorChecksum(b[1 : 1+2*WordsPerBlock])
	if b[len(b)-1] != wantCksum {
		t.Errorf("checksum 0x%02X, want 0x%02X", b[len(b)-1], wantCksum)
	}
	for i, x := range b {
		if x > 0x7F {
			t.Errorf("byte %d non-7-bit: 0x%02X", i, x)
		}
	}

	// Decode round-trip.
	num, gotWords, err := DecodeBlock(b)
	if err != nil {
		t.Fatal(err)
	}
	if num != 3 {
		t.Errorf("decoded num %d, want 3", num)
	}
	if gotWords != words {
		t.Errorf("words round-trip failed")
	}
}

func TestDecodeBlockDetectsCorruption(t *testing.T) {
	var words [WordsPerBlock]uint16
	b := EncodeBlock(0, words)
	b[5] ^= 0x01 // flip one data byte
	if _, _, err := DecodeBlock(b); err == nil {
		t.Error("expected checksum error after data flip")
	}
}

func TestBlockNumberWrap(t *testing.T) {
	// Block 128 wraps to LSB 0 on the wire.
	var words [WordsPerBlock]uint16
	b := EncodeBlock(128, words)
	if b[0] != 0 {
		t.Errorf("block 128 LSB = %d, want 0", b[0])
	}
	b = EncodeBlock(129, words)
	if b[0] != 1 {
		t.Errorf("block 129 LSB = %d, want 1", b[0])
	}
}

func TestNumBlocks(t *testing.T) {
	tests := []struct {
		words uint32
		want  int
	}{
		{0, 0},
		{1, 1},
		{60, 1},
		{61, 2},
		{120, 2},
		{121, 3},
		{475020, 7917},
	}
	for _, tt := range tests {
		if got := NumBlocks(tt.words); got != tt.want {
			t.Errorf("NumBlocks(%d) = %d, want %d", tt.words, got, tt.want)
		}
	}
}
