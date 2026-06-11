package device

import (
	"errors"
	"testing"
	"time"

	"github.com/bivers/s950/internal/protocol"
)

// Tests for the transport-aware timing layer: WaitTX (exact tcdrain
// on serial vs estimate-sleep on MIDI), DrainEstimate (real baud vs
// MIDI's 3125 B/s), and the per-transport NAK windows. The bug class
// this locks out: MIDI-era worst-case sleeps (200 ms floor, 3125 B/s)
// taxing every operation on a 50000-baud serial session.

// drainCapableTransport extends the basic send fake with the serial
// transport's optional capabilities.
type drainCapableTransport struct {
	fakeSendTransport
	rate      int
	waited    int
	waitErr   error
}

func (f *drainCapableTransport) WaitTX() error {
	f.waited++
	return f.waitErr
}
func (f *drainCapableTransport) WireRate() int { return f.rate }

func TestWaitTX_SerialSleepsHonestEstimateThenDrains(t *testing.T) {
	// Serial path: WaitTX = sleep(real-baud estimate) + tcdrain.
	// The estimate floor is load-bearing — tcdrain on USB-serial
	// returns early (kernel buffer only, not the adapter FIFO;
	// hardware-observed 1.4 s early on an 81 KB dump, crashing the
	// sampler). 250 bytes at 5000 B/s = 50 ms + 10% = 55 ms.
	f := &drainCapableTransport{rate: 5000}
	d := New(f, 0)
	start := time.Now()
	d.WaitTX(250)
	elapsed := time.Since(start)
	if elapsed < 50*time.Millisecond {
		t.Errorf("WaitTX returned after %v — the wire-time floor is gone (tcdrain-only would race the UART)", elapsed)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("WaitTX took %v — MIDI-rate sleep leaked into the serial path", elapsed)
	}
	if f.waited != 1 {
		t.Errorf("tcdrain called %d times, want 1", f.waited)
	}
}

func TestWaitTX_SerialKeepsFloorOnDrainError(t *testing.T) {
	// A failing tcdrain must not shorten the wait — the estimate
	// already covers the wire time.
	f := &drainCapableTransport{rate: 5000, waitErr: errors.New("ioctl failed")}
	d := New(f, 0)
	start := time.Now()
	d.WaitTX(500) // 100ms + 10% = 110ms
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("estimate floor not honored on drain error: %v", elapsed)
	}
}

func TestWaitTX_MIDIPathSleepsTheEstimate(t *testing.T) {
	d := New(&fakeSendTransport{}, 0) // no capabilities = MIDI behavior
	start := time.Now()
	d.WaitTX(1)
	if elapsed := time.Since(start); elapsed < 190*time.Millisecond {
		t.Errorf("MIDI path must keep the 200ms floor, returned after %v", elapsed)
	}
}

func TestDrainEstimate_UsesWireRate(t *testing.T) {
	f := &drainCapableTransport{rate: 5000} // 50000 baud
	d := New(f, 0)
	// 10000 bytes at 5000 B/s = 2s, +10% = 2.2s.
	got := d.DrainEstimate(10_000)
	want := 2200 * time.Millisecond
	if got != want {
		t.Errorf("DrainEstimate = %v, want %v", got, want)
	}
	// MIDI fallback: 10000 / 3125 = 3.2s, +10% = 3.52s.
	dm := New(&fakeSendTransport{}, 0)
	if got := dm.DrainEstimate(10_000); got != 3520*time.Millisecond {
		t.Errorf("MIDI DrainEstimate = %v, want 3.52s", got)
	}
}

func TestDrainEstimate_NoMIDIFloorOnSerial(t *testing.T) {
	// A 129-byte SPRM at 5000 B/s ≈ 28 ms — the 200 ms CoreMIDI
	// floor must NOT apply to the serial estimate.
	f := &drainCapableTransport{rate: 5000}
	d := New(f, 0)
	if got := d.DrainEstimate(129); got >= 200*time.Millisecond {
		t.Errorf("serial estimate %v should not carry the MIDI floor", got)
	}
}

func TestNAKWindow_PerTransport(t *testing.T) {
	serial := New(&drainCapableTransport{rate: 5000}, 0)
	if got := serial.NAKWindow(); got != 250*time.Millisecond {
		t.Errorf("serial NAK window = %v, want 250ms", got)
	}
	midi := New(&fakeSendTransport{}, 0)
	if got := midi.NAKWindow(); got != 500*time.Millisecond {
		t.Errorf("MIDI NAK window = %v, want 500ms", got)
	}
}

func TestSetParams_SerialAvoidsMIDIFloor(t *testing.T) {
	// The live-sync hot path: a 129-byte SPRM write on serial waits
	// its real wire time (~28ms at 50000 baud) instead of the MIDI
	// path's 200ms floor.
	f := &drainCapableTransport{rate: 5000}
	d := New(f, 0)
	p := &protocol.SampleParams{Name: "FASTEDIT  ", TotalWords: 100,
		SampleRateHz: 26040, NominalPitch: 960, ReplayMode: 'O', Reversed: 'N'}
	start := time.Now()
	if err := d.SetParams(0, p); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Errorf("SetParams over serial took %v — MIDI floor leaked back in", elapsed)
	}
	if f.waited != 1 {
		t.Errorf("WaitTX calls = %d, want 1", f.waited)
	}
}

func TestPutSampleOpenLoop_DrainEstimateIsTransportAware(t *testing.T) {
	words := make([]uint16, 6000)
	for i := range words {
		words[i] = 0x800
	}
	fSerial := &drainCapableTransport{rate: 5000}
	_, drainSerial, err := New(fSerial, 0).PutSampleOpenLoop(words, PutSampleOpts{SampleRateHz: 26040})
	if err != nil {
		t.Fatal(err)
	}
	fMidi := &fakeSendTransport{}
	_, drainMidi, err := New(fMidi, 0).PutSampleOpenLoop(words, PutSampleOpts{SampleRateHz: 26040})
	if err != nil {
		t.Fatal(err)
	}
	if drainSerial >= drainMidi {
		t.Errorf("serial estimate (%v) should be shorter than MIDI's (%v) at 50000 baud", drainSerial, drainMidi)
	}
}
