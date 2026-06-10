package transport

import (
	"bytes"
	"testing"
)

// Tests for the pure helpers split out of the midiTransport methods.
// pickPortIn/pickPortOut still need the gomidi/drivers interfaces,
// but the matching rule itself (pickPortIdx) is name-only and now
// table-testable.

func TestPickPortIdx_EmptyWantPicksFirst(t *testing.T) {
	idx := pickPortIdx([]string{"Foo", "Bar"}, "")
	if idx != 0 {
		t.Errorf("empty want should pick index 0, got %d", idx)
	}
}

func TestPickPortIdx_EmptyListReturnsMinusOne(t *testing.T) {
	if idx := pickPortIdx(nil, ""); idx != -1 {
		t.Errorf("nil list should return -1, got %d", idx)
	}
	if idx := pickPortIdx([]string{}, "anything"); idx != -1 {
		t.Errorf("empty list should return -1, got %d", idx)
	}
}

func TestPickPortIdx_CaseInsensitiveSubstring(t *testing.T) {
	names := []string{"MIDI A", "S950 Port", "USB Loopback"}
	cases := []struct {
		want string
		idx  int
	}{
		{"s950", 1},
		{"S950", 1},
		{"port", 1}, // first containing match wins
		{"USB", 2},
		{"usb loop", 2},
		{"MIDI A", 0},
	}
	for _, c := range cases {
		if got := pickPortIdx(names, c.want); got != c.idx {
			t.Errorf("pickPortIdx(_, %q) = %d, want %d", c.want, got, c.idx)
		}
	}
}

func TestPickPortIdx_NoMatchReturnsMinusOne(t *testing.T) {
	if idx := pickPortIdx([]string{"A", "B"}, "Z"); idx != -1 {
		t.Errorf("no-match should return -1, got %d", idx)
	}
}

func TestPopSysExEnvelope_HappyPath(t *testing.T) {
	in := []byte{0xF0, 0x47, 0x00, 0xF7, 0x90, 0x40}
	env, rest, ok := popSysExEnvelope(in)
	if !ok {
		t.Fatal("expected ok=true for a complete envelope")
	}
	wantEnv := []byte{0xF0, 0x47, 0x00, 0xF7}
	if !bytes.Equal(env, wantEnv) {
		t.Errorf("envelope = % X, want % X", env, wantEnv)
	}
	wantRest := []byte{0x90, 0x40}
	if !bytes.Equal(rest, wantRest) {
		t.Errorf("remaining = % X, want % X", rest, wantRest)
	}
}

func TestPopSysExEnvelope_NoF7YetReturnsBufUnchanged(t *testing.T) {
	// Partial envelope — the SysEx is mid-flight; the caller (rxQ)
	// keeps it in the buffer and tries again after more bytes land.
	in := []byte{0xF0, 0x47, 0x00, 0x40}
	env, rest, ok := popSysExEnvelope(in)
	if ok {
		t.Fatal("partial envelope should return ok=false")
	}
	if env != nil {
		t.Errorf("envelope should be nil on partial, got % X", env)
	}
	if !bytes.Equal(rest, in) {
		t.Errorf("remaining should equal input on partial, got % X want % X", rest, in)
	}
}

func TestPopSysExEnvelope_NonF0HeadRejected(t *testing.T) {
	// Defensive case: leading byte isn't F0. The transport's
	// popOneSysEx wraps this with a "clear the queue" cleanup.
	env, _, ok := popSysExEnvelope([]byte{0x90, 0xF0, 0xF7})
	if ok {
		t.Fatal("non-F0 head must not yield an envelope")
	}
	if env != nil {
		t.Errorf("env should be nil, got % X", env)
	}
}

func TestPopSysExEnvelope_EmptyBuffer(t *testing.T) {
	env, rest, ok := popSysExEnvelope(nil)
	if ok || env != nil || rest != nil {
		t.Errorf("empty input should yield (nil, nil, false); got (% X, % X, %v)", env, rest, ok)
	}
}

func TestPopSysExEnvelope_TwoBackToBackEnvelopes(t *testing.T) {
	// First pop yields envelope #1 + remaining = envelope #2. Second
	// pop on the remaining yields envelope #2. Pins the queue-style
	// usage popOneSysEx relies on.
	in := []byte{0xF0, 0x7E, 0x7F, 0xF7, 0xF0, 0x47, 0x42, 0xF7}
	env1, rest, ok := popSysExEnvelope(in)
	if !ok {
		t.Fatal("first pop should succeed")
	}
	want1 := []byte{0xF0, 0x7E, 0x7F, 0xF7}
	if !bytes.Equal(env1, want1) {
		t.Errorf("env1 = % X, want % X", env1, want1)
	}
	env2, rest2, ok := popSysExEnvelope(rest)
	if !ok {
		t.Fatal("second pop should succeed")
	}
	want2 := []byte{0xF0, 0x47, 0x42, 0xF7}
	if !bytes.Equal(env2, want2) {
		t.Errorf("env2 = % X, want % X", env2, want2)
	}
	if len(rest2) != 0 {
		t.Errorf("queue should be empty after two pops, got % X", rest2)
	}
}

func TestPopSysExEnvelope_EnvelopeIsIndependentCopy(t *testing.T) {
	// Mutating the original buffer after popping must not change the
	// returned envelope. Defends against shared-slice aliasing bugs
	// in the wire-log emitter (it retains the slice and the queue
	// must not corrupt it on the next push).
	in := []byte{0xF0, 0x47, 0x42, 0xF7}
	env, _, ok := popSysExEnvelope(in)
	if !ok {
		t.Fatal("expected ok=true")
	}
	in[1] = 0xFF // mutate source
	if env[1] != 0x47 {
		t.Errorf("envelope aliases source buffer: env[1] = %#x", env[1])
	}
}

func TestPrefixForLog_UnderCap(t *testing.T) {
	in := []byte{0x01, 0x02, 0x03}
	if got := prefixForLog(in); len(got) != 3 || got[2] != 0x03 {
		t.Errorf("prefixForLog(short) = % X, want unchanged input", got)
	}
}

func TestPrefixForLog_OverCap(t *testing.T) {
	in := make([]byte, 200)
	for i := range in {
		in[i] = byte(i)
	}
	got := prefixForLog(in)
	if len(got) != 64 {
		t.Errorf("prefixForLog(200 bytes) returned %d bytes, want 64", len(got))
	}
	for i, b := range got {
		if b != byte(i) {
			t.Errorf("prefix[%d] = %#x, want %#x", i, b, byte(i))
			break
		}
	}
}
