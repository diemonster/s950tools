package device

import (
	"strings"
	"testing"
	"time"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/transport"
)

// fakeStreamTransport implements transport.PartialRecvTransport with
// pre-scripted inbound bytes — the test loads the response envelope
// up front, then the device-layer code reads it back as if the S950
// were on the other end. Send() captures all outbound bytes so we
// can assert the ACK/RSD pattern.
type fakeStreamTransport struct {
	sent     [][]byte
	inbound  []byte
	cursor   int
	stream   bool
	beginCnt int
	endCnt   int
}

func (f *fakeStreamTransport) Send(b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	f.sent = append(f.sent, cp)
	return nil
}

func (f *fakeStreamTransport) RecvSysEx(_ time.Duration) ([]byte, error) {
	return nil, nil
}

func (f *fakeStreamTransport) Drain()             {}
func (f *fakeStreamTransport) Close() error       { return nil }
func (f *fakeStreamTransport) InName() string     { return "fake" }
func (f *fakeStreamTransport) OutName() string    { return "fake" }
func (f *fakeStreamTransport) BeginStream()       { f.stream = true; f.beginCnt++ }
func (f *fakeStreamTransport) EndStream()         { f.stream = false; f.endCnt++ }

func (f *fakeStreamTransport) RecvBytes(buf []byte, timeout time.Duration) (int, error) {
	// Mimic real serial timeout semantics: if we've exhausted the
	// scripted bytes, return a timeout error. The streaming
	// receiver's streamReadFull loops on partial reads, so we deliver
	// up to len(buf) bytes per call.
	if f.cursor >= len(f.inbound) {
		return 0, &timeoutErr{msg: "fake eof"}
	}
	n := copy(buf, f.inbound[f.cursor:])
	f.cursor += n
	return n, nil
}

// Compile-time assertion: the fake satisfies the interface the
// dispatch path checks. Without this, getSampleAudioStreaming would
// be silently skipped (falling back to the pump path).
var _ transport.PartialRecvTransport = (*fakeStreamTransport)(nil)

type timeoutErr struct{ msg string }

func (e *timeoutErr) Error() string { return e.msg }

// buildPaddedDumpBytes constructs the wire bytes a device emits when
// it pads the final block to the full 60-word block size. Same shape
// as buildEnvelope from getsample_test.go but factored to take a
// per-block fill pattern so tests can verify words round-trip.
func buildPaddedDumpBytes(slot uint16, totalWords uint32, fillWord uint16) []byte {
	hdr := protocol.SampleDumpHeader{
		Num:         slot,
		BitsPerWord: 12,
		PeriodNS:    22676,
		TotalWords:  totalWords,
		LoopStart:   totalWords - 5,
		LoopEnd:     totalWords - 1,
		Mode:        0,
	}
	out := append([]byte(nil), hdr.EncodeHeader()...)
	n := protocol.NumBlocks(totalWords)
	for i := 0; i < n; i++ {
		var blk [protocol.WordsPerBlock]uint16
		for j := 0; j < protocol.WordsPerBlock; j++ {
			if uint32(i*protocol.WordsPerBlock+j) < totalWords {
				blk[j] = fillWord
			} else {
				blk[j] = 0x800 // silence padding
			}
		}
		out = append(out, protocol.EncodeBlock(i, blk)...)
	}
	return append(out, protocol.EOX)
}

// buildTruncatedDumpBytes is the same as buildPaddedDumpBytes but
// emits the final partial block as 1+rem*2+1 bytes (the truncated
// form some S950 firmware revs use — see ParseSampleDumpEnvelope's
// inline comment about the 164060-word case being 6 bytes short).
func buildTruncatedDumpBytes(slot uint16, totalWords uint32, fillWord uint16) []byte {
	hdr := protocol.SampleDumpHeader{
		Num:         slot,
		BitsPerWord: 12,
		PeriodNS:    22676,
		TotalWords:  totalWords,
		LoopStart:   totalWords - 5,
		LoopEnd:     totalWords - 1,
		Mode:        0,
	}
	out := append([]byte(nil), hdr.EncodeHeader()...)
	full := int(totalWords / protocol.WordsPerBlock)
	rem := int(totalWords % protocol.WordsPerBlock)
	for i := 0; i < full; i++ {
		var blk [protocol.WordsPerBlock]uint16
		for j := 0; j < protocol.WordsPerBlock; j++ {
			blk[j] = fillWord
		}
		out = append(out, protocol.EncodeBlock(i, blk)...)
	}
	if rem > 0 {
		// Hand-assemble a partial block: 1 byte block# + 2 bytes per
		// SW word + 1 byte checksum (XOR over the data bytes only).
		partial := make([]byte, 1+rem*2+1)
		partial[0] = byte(full & 0x7F)
		data := partial[1 : 1+rem*2]
		for j := 0; j < rem; j++ {
			lo, hi := encodeSW(fillWord)
			data[j*2] = lo
			data[j*2+1] = hi
		}
		partial[len(partial)-1] = xorChecksum(data)
		out = append(out, partial...)
	}
	return append(out, protocol.EOX)
}

// encodeSW / xorChecksum are tiny re-derivations of the sysex
// primitives so the test file doesn't have to thread a dependency
// on internal/sysex. They MUST match the production layout from
// sysex.EncodeSW: byte0 = top 7 bits, byte1 = bottom 5 shifted up.
// Verified by the happy-path test reading the encoded body back
// through DecodeBlock.
func encodeSW(w uint16) (b0, b1 byte) {
	w &= 0x0FFF
	return byte((w >> 5) & 0x7F), byte((w << 2) & 0x7F)
}
func xorChecksum(data []byte) byte {
	var x byte
	for _, b := range data {
		x ^= b
	}
	return x & 0x7F
}

// newFakeDevice wires a fakeStreamTransport into a Device the way
// production code does. The fake satisfies PartialRecvTransport, so
// GetSampleAudio's dispatch picks the streaming receive path.
func newFakeDevice(inbound []byte) (*Device, *fakeStreamTransport) {
	t := &fakeStreamTransport{inbound: inbound}
	return New(t, 0), t
}

// ---------- Happy-path: padded last block ----------

func TestGetSampleAudioStreaming_PaddedLastBlock(t *testing.T) {
	const slot = byte(7)
	const total = uint32(150) // 3 blocks (60 + 60 + 30 with 30 padding)
	inbound := buildPaddedDumpBytes(uint16(slot), total, 0x123)

	d, fake := newFakeDevice(inbound)
	hdr, words, err := d.GetSampleAudio(slot, 2*time.Second)
	if err != nil {
		t.Fatalf("GetSampleAudio: %v", err)
	}

	if hdr.Num != uint16(slot) {
		t.Errorf("hdr.Num = %d, want %d", hdr.Num, slot)
	}
	if hdr.TotalWords != total {
		t.Errorf("hdr.TotalWords = %d, want %d", hdr.TotalWords, total)
	}
	if uint32(len(words)) != total {
		t.Fatalf("len(words) = %d, want %d", len(words), total)
	}
	for i, w := range words {
		if w != 0x123 {
			t.Errorf("words[%d] = %#x, want 0x123", i, w)
			break
		}
	}

	// Send pattern: 1 RSD + 1 post-header ACK + 1 ACK per block.
	expectedSends := 1 + 1 + protocol.NumBlocks(total)
	if len(fake.sent) != expectedSends {
		t.Fatalf("sent %d messages, want %d", len(fake.sent), expectedSends)
	}
	if fake.sent[0][2] != protocol.CodeRSD {
		t.Errorf("first send was not RSD: % X", fake.sent[0])
	}
	ack := protocol.BuildHandshake(protocol.CodeACKS)
	for i := 1; i < len(fake.sent); i++ {
		if string(fake.sent[i]) != string(ack) {
			t.Errorf("send[%d] = % X, want ACK", i, fake.sent[i])
		}
	}

	// Stream mode was entered exactly once and exited exactly once
	// (via the function's defer).
	if fake.beginCnt != 1 || fake.endCnt != 1 {
		t.Errorf("BeginStream/EndStream counts: %d/%d, want 1/1", fake.beginCnt, fake.endCnt)
	}
}

// ---------- Happy-path: truncated last block ----------

func TestGetSampleAudioStreaming_TruncatedLastBlock(t *testing.T) {
	// 80 words = 1 full block (60) + 20-word partial. Different
	// firmware revs send this last block truncated (1+20*2+1 = 42
	// bytes) instead of padded to 122. Streaming receive must peek
	// for F7 after partial-size and fall through correctly.
	const slot = byte(2)
	const total = uint32(80)
	inbound := buildTruncatedDumpBytes(uint16(slot), total, 0x456)

	d, _ := newFakeDevice(inbound)
	hdr, words, err := d.GetSampleAudio(slot, 2*time.Second)
	if err != nil {
		t.Fatalf("GetSampleAudio: %v", err)
	}
	if hdr.TotalWords != total {
		t.Errorf("hdr.TotalWords = %d, want %d", hdr.TotalWords, total)
	}
	if uint32(len(words)) != total {
		t.Fatalf("len(words) = %d, want %d", len(words), total)
	}
	for i, w := range words {
		if w != 0x456 {
			t.Errorf("words[%d] = %#x, want 0x456", i, w)
			break
		}
	}
}

// ---------- Edge: device dump ends exactly on a block boundary ----------

func TestGetSampleAudioStreaming_ExactBlockBoundary(t *testing.T) {
	// totalWords % 60 == 0 → no partial block. The streaming
	// receive's "last block" branch never fires; the loop exits
	// cleanly after the final full block, then reads F7.
	const slot = byte(0)
	const total = uint32(120) // exactly 2 full blocks
	inbound := buildPaddedDumpBytes(uint16(slot), total, 0x789)

	d, _ := newFakeDevice(inbound)
	_, words, err := d.GetSampleAudio(slot, 2*time.Second)
	if err != nil {
		t.Fatalf("GetSampleAudio: %v", err)
	}
	if uint32(len(words)) != total {
		t.Fatalf("len(words) = %d, want %d", len(words), total)
	}
}

// ---------- Error paths ----------

func TestGetSampleAudioStreaming_DeviceNAK(t *testing.T) {
	// Device responds with F0 7E 7E F7 (NAK) instead of a dump.
	inbound := []byte{protocol.SOX, protocol.UniversalNRT, protocol.CodeNAKS, protocol.EOX}
	d, _ := newFakeDevice(inbound)
	_, _, err := d.GetSampleAudio(0, 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "NAK") {
		t.Fatalf("expected NAK error, got %v", err)
	}
}

func TestGetSampleAudioStreaming_DeviceAbort(t *testing.T) {
	inbound := []byte{protocol.SOX, protocol.UniversalNRT, protocol.CodeASD, protocol.EOX}
	d, _ := newFakeDevice(inbound)
	_, _, err := d.GetSampleAudio(0, 500*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "abort") {
		t.Fatalf("expected abort error, got %v", err)
	}
}

func TestGetSampleAudioStreaming_WrongSlotInHeader(t *testing.T) {
	// Device sends a dump for slot 3, but we asked for slot 5.
	// Catches misrouted dumps before they overwrite the wrong row.
	inbound := buildPaddedDumpBytes(3, 60, 0x800)
	d, _ := newFakeDevice(inbound)
	_, _, err := d.GetSampleAudio(5, 1*time.Second)
	if err == nil || !strings.Contains(err.Error(), "slot 3") {
		t.Fatalf("expected slot-mismatch error naming slot 3, got %v", err)
	}
}

func TestGetSampleAudioStreaming_TimesOutOnMissingTail(t *testing.T) {
	// Strip the trailing F7 — the device never closes the envelope.
	// streamReadFull should hit the (short) deadline and surface
	// the error.
	inbound := buildPaddedDumpBytes(0, 60, 0x800)
	inbound = inbound[:len(inbound)-1] // drop F7
	d, _ := newFakeDevice(inbound)
	_, _, err := d.GetSampleAudio(0, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for missing trailing F7")
	}
}

func TestGetSampleAudioStreaming_UnexpectedSubCode(t *testing.T) {
	// F0 7E 02 F7 — same universal-NRT prefix but sub-code 02 isn't
	// CodeSD / CodeNAKS / CodeASD. Function should refuse.
	inbound := []byte{protocol.SOX, protocol.UniversalNRT, 0x02, protocol.EOX}
	d, _ := newFakeDevice(inbound)
	_, _, err := d.GetSampleAudio(0, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "sub-code") {
		t.Fatalf("expected sub-code error, got %v", err)
	}
}
