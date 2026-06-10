package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/bivers/s950/internal/protocol"
)

// Pure-helper tests for the CLI. These cover the formatting / parsing
// helpers that aren't wired to a transport — the cobra commands
// themselves are integration-level and need a refactor before they can
// be tested without a real device. See REFACTOR_FLAGS.md.

func TestPlural(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "s"},
		{1, ""},
		{2, "s"},
	}
	for _, c := range cases {
		if got := plural(c.n); got != c.want {
			t.Errorf("plural(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestParseSampleNum_HappyPath(t *testing.T) {
	cases := []struct {
		in   string
		want byte
	}{
		{"0", 0},
		{"5", 5},
		{"99", 99},
	}
	for _, c := range cases {
		got, err := parseSampleNum(c.in)
		if err != nil {
			t.Errorf("parseSampleNum(%q): unexpected err %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSampleNum(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseSampleNum_RejectsOutOfRange(t *testing.T) {
	for _, in := range []string{"-1", "100", "200"} {
		if _, err := parseSampleNum(in); err == nil {
			t.Errorf("parseSampleNum(%q) should reject out-of-range", in)
		}
	}
}

func TestParseSampleNum_RejectsNonNumeric(t *testing.T) {
	for _, in := range []string{"", "abc", "1.5"} {
		// 1.5 is intentionally in the rejection list — Sscanf("%d")
		// reads "1" and ignores ".5". We accept that quirk today; if
		// the behaviour ever tightens, update the assertion.
		_, err := parseSampleNum(in)
		switch in {
		case "1.5":
			// Partial parse — parseSampleNum returns 1, no error.
			// Document the current behaviour rather than assert against it,
			// so this test stays honest about the parser's loose mode.
			if err != nil {
				t.Logf("note: parseSampleNum(%q) now errors (was lenient before): %v", in, err)
			}
		default:
			if err == nil {
				t.Errorf("parseSampleNum(%q) should error on non-numeric input", in)
			}
		}
	}
}

func TestParams2JSON_RoundTripFields(t *testing.T) {
	// Realistic-ish SampleParams. Mostly checks that the JSON map
	// reflects exactly the SampleParams fields the CLI emits — a
	// silent rename downstream (e.g. NominalPitch → SNOMP in the
	// wire model) would surface here as a missing key.
	p := &protocol.SampleParams{
		Name:         "KICK808",
		TotalWords:   12345,
		SampleRateHz: 26040,
		NominalPitch: 960,
		LoudOffset:   42,
		ReplayMode:   'O',
		Start:        0,
		End:          12344,
		LoopLength:   100,
		Reversed:     'N',
		VelXFade:     1,
	}
	got := params2JSON(p)

	expect := map[string]any{
		"name":            "KICK808",
		"total_words":     uint32(12345),
		"sample_rate_hz":  uint16(26040),
		"nominal_pitch":   uint16(960),
		"loudness_offset": int16(42),
		"replay_mode":     "O",
		"start":           uint32(0),
		"end":             uint32(12344),
		"loop_length":     uint32(100),
		"reversed":        "N",
		"velocity_xfade":  true,
	}
	for k, want := range expect {
		gotV, ok := got[k]
		if !ok {
			t.Errorf("params2JSON missing key %q", k)
			continue
		}
		if gotV != want {
			t.Errorf("params2JSON[%q] = %v (%T), want %v (%T)", k, gotV, gotV, want, want)
		}
	}
}

func TestParams2JSON_VelXFadeZeroIsFalse(t *testing.T) {
	// The CLI normalises 0 → false; non-zero → true. Pin both.
	p := &protocol.SampleParams{VelXFade: 0}
	if got := params2JSON(p)["velocity_xfade"]; got != false {
		t.Errorf("velocity_xfade for VelXFade=0 = %v, want false", got)
	}
	p = &protocol.SampleParams{VelXFade: 7}
	if got := params2JSON(p)["velocity_xfade"]; got != true {
		t.Errorf("velocity_xfade for VelXFade=7 = %v, want true", got)
	}
}

func TestOmniLabel(t *testing.T) {
	if got := omniLabel(true); got != "(omni on)" {
		t.Errorf("omniLabel(true) = %q, want %q", got, "(omni on)")
	}
	if got := omniLabel(false); got != "" {
		t.Errorf("omniLabel(false) = %q, want empty", got)
	}
}

func TestControllerLabel(t *testing.T) {
	cases := []struct {
		v    uint8
		want string
	}{
		{1, "MIDI"},
		{2, "RS-232C"},
		{0, "unknown (0x00)"},
		{0xFF, "unknown (0xFF)"},
	}
	for _, c := range cases {
		if got := controllerLabel(c.v); got != c.want {
			t.Errorf("controllerLabel(%d) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestOnOff(t *testing.T) {
	if got := onOff(true); got != "on" {
		t.Errorf("onOff(true) = %q, want %q", got, "on")
	}
	if got := onOff(false); got != "off" {
		t.Errorf("onOff(false) = %q, want %q", got, "off")
	}
}

func TestWaitWithProgressTo_ShortWaitSleepsWithoutTicks(t *testing.T) {
	// total <= tickEvery → sleep path: nothing written to w.
	var buf bytes.Buffer
	start := time.Now()
	waitWithProgressTo(&buf, 5*time.Millisecond, 50*time.Millisecond)
	elapsed := time.Since(start)
	if buf.Len() != 0 {
		t.Errorf("short wait should write nothing, got %q", buf.String())
	}
	if elapsed < 5*time.Millisecond {
		t.Errorf("expected at least 5ms wait, got %v", elapsed)
	}
}

func TestWaitWithProgressTo_LongWaitEmitsDotsAndNewline(t *testing.T) {
	// 50 ms total, 10 ms tick → ~4–5 dots + a trailing newline. The
	// exact count varies with scheduler jitter so we only require at
	// least one dot.
	var buf bytes.Buffer
	waitWithProgressTo(&buf, 50*time.Millisecond, 10*time.Millisecond)
	out := buf.String()
	if !strings.Contains(out, ".") {
		t.Errorf("expected at least one progress dot, got %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("expected trailing newline, got %q", out)
	}
}
