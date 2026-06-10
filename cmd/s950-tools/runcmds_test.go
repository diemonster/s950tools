package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
)

// Tests for the cobra `run<Cmd>` cores extracted from RunE bodies.
// The cobra wrapper opens a real transport and immediately hands off
// to these functions — exercising them with a fake here pins the
// table/JSON formatting + the error-propagation paths without
// rtmidi or a real S950.

// fakeReplyTransport is a minimal transport.Transport that replies
// with a single canned SysEx envelope to every RecvSysEx call. The
// caller's loop (Device.Catalog, GetParams etc) iterates RecvSysEx
// until ParseAkai matches the expected function code; since our
// reply is always the right code, the first call resolves it.
type fakeReplyTransport struct {
	sent  [][]byte
	reply []byte
}

func (f *fakeReplyTransport) Send(b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	f.sent = append(f.sent, cp)
	return nil
}
func (f *fakeReplyTransport) RecvSysEx(_ time.Duration) ([]byte, error) {
	if f.reply == nil {
		return nil, errors.New("simulated timeout")
	}
	return f.reply, nil
}
func (f *fakeReplyTransport) Drain()          {}
func (f *fakeReplyTransport) Close() error    { return nil }
func (f *fakeReplyTransport) InName() string  { return "fake" }
func (f *fakeReplyTransport) OutName() string { return "fake" }

// buildCatalogEntry packs one 12-byte CAT entry: type, num, then 10
// space-padded ASCII chars. Used to seed fakeReplyTransport with a
// realistic CAT payload.
func buildCatalogEntry(typ byte, num byte, name string) []byte {
	out := make([]byte, 12)
	out[0] = typ
	out[1] = num
	// Pad name to 10 chars with spaces (ParseCatalog trims trailing
	// space + NUL).
	padded := name
	if len(padded) > 10 {
		padded = padded[:10]
	}
	for i := 0; i < 10; i++ {
		if i < len(padded) {
			out[2+i] = padded[i]
		} else {
			out[2+i] = ' '
		}
	}
	return out
}

func newFakeDeviceWithReply(reply []byte) (*device.Device, *fakeReplyTransport) {
	t := &fakeReplyTransport{reply: reply}
	return device.New(t, 0), t
}

func TestRunCatalog_RendersTable(t *testing.T) {
	// Build a synthetic CAT envelope: 2 sample entries + 1 program.
	payload := bytes.Join([][]byte{
		buildCatalogEntry('S', 0, "KICK"),
		buildCatalogEntry('S', 1, "SNARE"),
		buildCatalogEntry('P', 5, "MYPROG"),
	}, nil)
	envelope := protocol.BuildAkaiData(0, protocol.FuncCAT, 0, payload)

	d, fake := newFakeDeviceWithReply(envelope)
	var out bytes.Buffer
	if err := runCatalog(&out, d); err != nil {
		t.Fatalf("runCatalog: %v", err)
	}

	got := out.String()
	for _, want := range []string{"TYPE", "NUM", "NAME", "SMP", "KICK", "SNARE", "PRG", "MYPROG"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in catalog output, got:\n%s", want, got)
		}
	}
	// One RCAT request should have hit the wire.
	if len(fake.sent) != 1 {
		t.Errorf("expected 1 send (RCAT), got %d", len(fake.sent))
	}
}

func TestRunCatalog_PropagatesTransportError(t *testing.T) {
	// reply == nil → RecvSysEx times out → Device.Catalog returns an
	// error. runCatalog must surface it (the cobra wrapper then
	// reports it to the user via os.Exit(1) in main).
	d, _ := newFakeDeviceWithReply(nil)
	d.RequestTimeout = 10 * time.Millisecond
	var out bytes.Buffer
	err := runCatalog(&out, d)
	if err == nil {
		t.Fatal("expected error when device doesn't reply")
	}
}

func TestRunGetParams_TableFormat(t *testing.T) {
	// Build an SPRM envelope (function = FuncSPRM, payload = 120 bytes)
	// — encode a SampleParams via its EncodePayload helper to make the
	// round-trip realistic.
	p := &protocol.SampleParams{
		Name:         "TESTSPRM",
		TotalWords:   1234,
		SampleRateHz: 22050,
		NominalPitch: 960,
		LoudOffset:   10,
		ReplayMode:   'O',
		Start:        0,
		End:          1233,
		LoopLength:   500,
		Reversed:     'N',
	}
	payload := p.EncodePayload()
	envelope := protocol.BuildAkaiData(0, protocol.FuncSPRM, 5, payload)

	d, _ := newFakeDeviceWithReply(envelope)
	var out bytes.Buffer
	if err := runGetParams(&out, d, 5, false); err != nil {
		t.Fatalf("runGetParams: %v", err)
	}
	got := out.String()
	for _, want := range []string{"Sample 5", "TESTSPRM", "total words", "1234", "sample rate", "22050"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in params output, got:\n%s", want, got)
		}
	}
}

func TestRunGetParams_JSONFormat(t *testing.T) {
	p := &protocol.SampleParams{
		Name:         "JSONSPRM",
		TotalWords:   42,
		SampleRateHz: 26040,
		NominalPitch: 960,
		ReplayMode:   'L',
	}
	envelope := protocol.BuildAkaiData(0, protocol.FuncSPRM, 3, p.EncodePayload())
	d, _ := newFakeDeviceWithReply(envelope)
	var out bytes.Buffer
	if err := runGetParams(&out, d, 3, true); err != nil {
		t.Fatalf("runGetParams(json): %v", err)
	}
	got := out.String()
	// The JSON view is keyed by snake_case fields — pin a few.
	for _, want := range []string{`"name":`, `"JSONSPRM"`, `"total_words"`, `"sample_rate_hz"`} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in JSON output, got:\n%s", want, got)
		}
	}
}

func TestPrintOverallTo_WritesAllFieldLabels(t *testing.T) {
	o := &protocol.OverallSettings{
		ProgName:         "PROG",
		BasicChannel:     1,
		OmniOn:           true,
		MidiTxChannel:    2,
		ControllerSelect: 2, // RS-232C
		BaudRate:         38400,
		PitchWheelRange:  2,
		LoudnessOnCC7:    true,
		MPEN:             false,
		RxSimChannel:     0,
		RxSimKey:         60,
		RxSimVelocity:    100,
	}
	var buf bytes.Buffer
	printOverallTo(&buf, o)
	got := buf.String()
	for _, want := range []string{
		"Overall Settings", "prog name", "basic ch", "midi tx ch",
		"ctrl mode", "rs-232 baud", "pitch wheel", "loudness CC7",
		"rx-sim ch/k/v", "RS-232C", "38400", "omni on",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in overall output, got:\n%s", want, got)
		}
	}
}

func TestPrintParamsTo_OmitsVelXFadeWhenZero(t *testing.T) {
	// VelXFade=0 → the "velocity xf" line must not be present (the
	// CLI hides it for non-crossfaded samples). Pin both branches.
	p := &protocol.SampleParams{Name: "X", VelXFade: 0}
	var buf bytes.Buffer
	printParamsTo(&buf, p, 0)
	if strings.Contains(buf.String(), "velocity xf") {
		t.Errorf("VelXFade=0 must not render the line; got:\n%s", buf.String())
	}

	buf.Reset()
	p.VelXFade = 1
	printParamsTo(&buf, p, 0)
	if !strings.Contains(buf.String(), "velocity xf : on") {
		t.Errorf("VelXFade=1 must render 'velocity xf : on'; got:\n%s", buf.String())
	}
}

// Sanity: the fakeReplyTransport's Sent() history captures exactly the
// AKAI requests our run cores produce. Lets future tests assert on
// outbound bytes without re-deriving the envelope shape.
func TestFakeReplyTransport_RecordsSends(t *testing.T) {
	envelope := protocol.BuildAkaiData(0, protocol.FuncCAT, 0, []byte{})
	_, fake := newFakeDeviceWithReply(envelope)
	// Wire a one-off send via the transport (no Device involvement).
	if err := fake.Send([]byte{0xF0, 0x47, 0xF7}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(fake.sent) != 1 || fake.sent[0][0] != 0xF0 {
		t.Errorf("Send not recorded properly: %v", fake.sent)
	}
}
