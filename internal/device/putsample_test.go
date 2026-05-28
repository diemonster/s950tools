package device

import (
	"errors"
	"testing"
	"time"

	"github.com/bivers/s950/internal/sample"
)

// fakeSendTransport captures bytes passed to Send for shape
// inspection. The streaming-test fake handles inbound; this one
// covers the outbound side without re-mocking the full interface.
type fakeSendTransport struct {
	sent    [][]byte
	sendErr error
}

func (f *fakeSendTransport) Send(b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	f.sent = append(f.sent, cp)
	return f.sendErr
}
func (f *fakeSendTransport) RecvSysEx(_ time.Duration) ([]byte, error) { return nil, nil }
func (f *fakeSendTransport) Drain()                                    {}
func (f *fakeSendTransport) Close() error                              { return nil }
func (f *fakeSendTransport) InName() string                            { return "fake" }
func (f *fakeSendTransport) OutName() string                           { return "fake" }

// PutSampleOpenLoop is the wire-bound half of every Send to S950
// upload (sample tab) and Apply Slicing. Failures here corrupt
// hardware uploads; worth testing the encoding contract.

func TestPutSampleOpenLoop_RejectsTooShort(t *testing.T) {
	d := New(&fakeSendTransport{}, 0)
	tiny := make([]uint16, sample.MinTotalWords-1)
	_, _, err := d.PutSampleOpenLoop(tiny, PutSampleOpts{Num: 0, SampleRateHz: 26040})
	if err == nil {
		t.Fatal("expected ErrLengthTooShort for sub-minimum length")
	}
	if !errors.Is(err, sample.ErrLengthTooShort) {
		t.Errorf("err = %v, want ErrLengthTooShort", err)
	}
}

func TestPutSampleOpenLoop_RejectsTooLong(t *testing.T) {
	d := New(&fakeSendTransport{}, 0)
	huge := make([]uint16, sample.MaxTotalWords+1)
	_, _, err := d.PutSampleOpenLoop(huge, PutSampleOpts{Num: 0, SampleRateHz: 26040})
	if err == nil {
		t.Fatal("expected ErrLengthTooLong for over-maximum length")
	}
}

func TestPutSampleOpenLoop_RejectsBadSampleRate(t *testing.T) {
	d := New(&fakeSendTransport{}, 0)
	words := make([]uint16, 1000)
	// 1 Hz is well below the S950's minimum (~2 kHz). HzToPeriodNS
	// should reject and PutSampleOpenLoop should surface that.
	_, _, err := d.PutSampleOpenLoop(words, PutSampleOpts{Num: 0, SampleRateHz: 1})
	if err == nil {
		t.Fatal("expected rate-out-of-range error")
	}
}

func TestPutSampleOpenLoop_HappyPath_EnvelopeRoundTrips(t *testing.T) {
	// Build a 150-word sample → 3 blocks (60 + 60 + 30, last padded).
	// Send via PutSampleOpenLoop, then re-parse the captured bytes
	// through ParseSampleDumpEnvelope. The result must match what
	// we sent in: same slot, same TotalWords, same payload.
	fake := &fakeSendTransport{}
	d := New(fake, 0)
	words := make([]uint16, 1000)
	for i := range words {
		words[i] = uint16(0x800 + (i & 0xFF)) // visibly distinct payload
	}
	const slot = byte(5)
	const rateHz = uint32(22050)
	sent, drain, err := d.PutSampleOpenLoop(words, PutSampleOpts{
		Num:          slot,
		SampleRateHz: rateHz,
		LoopStart:    100,
		LoopEnd:      200,
		Mode:         0, // looping
	})
	if err != nil {
		t.Fatalf("PutSampleOpenLoop: %v", err)
	}
	if drain <= 0 {
		t.Errorf("drain time = %v, want > 0", drain)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("Send called %d times, want 1", len(fake.sent))
	}
	if sent != len(fake.sent[0]) {
		t.Errorf("returned sent=%d, actual envelope=%d", sent, len(fake.sent[0]))
	}

	// Re-parse the on-wire envelope. The parser is unit-tested
	// elsewhere; this verifies our encoder produces something the
	// parser accepts — the closest we can get to a wire-format
	// round-trip without a real device.
	hdr, decoded, err := ParseSampleDumpEnvelope(fake.sent[0], slot)
	if err != nil {
		t.Fatalf("Parse own envelope: %v", err)
	}
	if hdr.Num != uint16(slot) {
		t.Errorf("hdr.Num = %d, want %d", hdr.Num, slot)
	}
	if hdr.TotalWords != uint32(len(words)) {
		t.Errorf("hdr.TotalWords = %d, want %d", hdr.TotalWords, len(words))
	}
	if len(decoded) != len(words) {
		t.Fatalf("decoded len = %d, want %d", len(decoded), len(words))
	}
	for i := range words {
		if decoded[i] != words[i] {
			t.Errorf("word[%d] = %#x, want %#x", i, decoded[i], words[i])
			break
		}
	}
}

func TestPutSampleOpenLoop_OneShotEncodedWhenLoopWindowSmall(t *testing.T) {
	// loopEnd <= loopStart+5 is the S950's one-shot sentinel. When
	// the caller passes 0/0 (the imported-sample default), the
	// encoder must rewrite loopStart/loopEnd into the
	// "total-5..total-1" pair the device treats as one-shot.
	fake := &fakeSendTransport{}
	d := New(fake, 0)
	words := make([]uint16, 1000)
	_, _, err := d.PutSampleOpenLoop(words, PutSampleOpts{
		Num:          0,
		SampleRateHz: 26040,
		LoopStart:    0,
		LoopEnd:      0,
	})
	if err != nil {
		t.Fatalf("PutSampleOpenLoop: %v", err)
	}
	hdr, _, err := ParseSampleDumpEnvelope(fake.sent[0], 0)
	if err != nil {
		t.Fatalf("Parse own envelope: %v", err)
	}
	wantStart := uint32(len(words)) - 5
	wantEnd := uint32(len(words)) - 1
	if hdr.LoopStart != wantStart || hdr.LoopEnd != wantEnd {
		t.Errorf("one-shot encoding: LoopStart=%d LoopEnd=%d, want %d/%d",
			hdr.LoopStart, hdr.LoopEnd, wantStart, wantEnd)
	}
}

func TestPutSampleOpenLoop_PropagatesSendError(t *testing.T) {
	wantErr := errors.New("driver hung up")
	fake := &fakeSendTransport{sendErr: wantErr}
	d := New(fake, 0)
	words := make([]uint16, 1000)
	_, _, err := d.PutSampleOpenLoop(words, PutSampleOpts{Num: 0, SampleRateHz: 26040})
	if err == nil || !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want underlying %v", err, wantErr)
	}
}
