package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bivers/s950/internal/protocol"
)

// Tests for the second batch of run* extractions: get/set OVS,
// get/set DRS, set-baud, parseBaudRate. Reuses fakeReplyTransport
// from runcmds_test.go (same package).

func buildOVSReply(o *protocol.OverallSettings) ([]byte, error) {
	payload, err := o.EncodePayload()
	if err != nil {
		return nil, err
	}
	return protocol.BuildAkaiData(0, protocol.FuncOVS, 0, payload), nil
}

func TestRunGetOverall_TableFormat(t *testing.T) {
	o := &protocol.OverallSettings{
		ProgName:         "BOOT",
		BasicChannel:     1,
		OmniOn:           true,
		MidiTxChannel:    2,
		ControllerSelect: 2,
		BaudRate:         38400,
		PitchWheelRange:  4,
		LoudnessOnCC7:    true,
		RxSimKey:         60,
		RxSimVelocity:    100,
	}
	env, err := buildOVSReply(o)
	if err != nil {
		t.Fatalf("buildOVSReply: %v", err)
	}
	d, _ := newFakeDeviceWithReply(env)
	var out bytes.Buffer
	if err := runGetOverall(&out, d, false); err != nil {
		t.Fatalf("runGetOverall: %v", err)
	}
	got := out.String()
	for _, want := range []string{"Overall Settings", "BOOT", "38400", "RS-232C", "omni on"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in table output, got:\n%s", want, got)
		}
	}
}

func TestRunGetOverall_JSONFormat(t *testing.T) {
	o := &protocol.OverallSettings{
		ProgName:        "JSONONE",
		BasicChannel:    3,
		MidiTxChannel:   4,
		BaudRate:        76800,
		PitchWheelRange: 2,
	}
	env, err := buildOVSReply(o)
	if err != nil {
		t.Fatalf("buildOVSReply: %v", err)
	}
	d, _ := newFakeDeviceWithReply(env)
	var out bytes.Buffer
	if err := runGetOverall(&out, d, true); err != nil {
		t.Fatalf("runGetOverall(json): %v", err)
	}
	// JSON output should be parseable back into OverallSettings.
	var back protocol.OverallSettings
	if err := json.Unmarshal(out.Bytes(), &back); err != nil {
		t.Fatalf("returned JSON not parseable: %v\n%s", err, out.String())
	}
	// ProgName is padded to 10 chars on encode; the decode side is
	// the JSON parser, not OVS-decode, so trailing spaces survive.
	// Trim for the comparison.
	if strings.TrimRight(back.ProgName, " ") != "JSONONE" || back.BaudRate != 76800 {
		t.Errorf("round-tripped JSON wrong shape: %+v", back)
	}
}

func TestRunSetOverall_RejectsInvalidJSON(t *testing.T) {
	d, fake := newFakeDeviceWithReply(nil)
	err := runSetOverall(d, []byte("{not json"))
	if err == nil {
		t.Fatal("expected JSON parse error")
	}
	// Invalid JSON must short-circuit before any wire write.
	if len(fake.sent) != 0 {
		t.Errorf("nothing should hit the wire on bad JSON, got %d sends", len(fake.sent))
	}
}

func TestRunSetOverall_WritesEnvelopeOnValidJSON(t *testing.T) {
	// Round-trip: encode → marshal → runSetOverall → fake captures
	// the AKAI-encoded payload. The captured envelope must be an
	// FuncOVS write at the expected channel/num.
	o := &protocol.OverallSettings{ProgName: "ROUNDTRP", BaudRate: 38400}
	j, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	d, fake := newFakeDeviceWithReply(nil)
	if err := runSetOverall(d, j); err != nil {
		t.Fatalf("runSetOverall: %v", err)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("expected 1 send (the OVS write), got %d", len(fake.sent))
	}
	env := fake.sent[0]
	// Envelope shape: F0 47 <ch> <FuncOVS> <DeviceID> <num> 00 <payload> <sum> F7
	if env[0] != 0xF0 || env[1] != 0x47 {
		t.Errorf("expected AKAI exclusive header, got % X", env[:2])
	}
	if env[3] != protocol.FuncOVS {
		t.Errorf("expected function = FuncOVS (%#x), got %#x", protocol.FuncOVS, env[3])
	}
}

func buildDRSReply(payload []byte) []byte {
	return protocol.BuildAkaiData(0, protocol.FuncDRS, 0, payload)
}

func TestRunGetDrum_NoOutputPrintsSize(t *testing.T) {
	// 480 zero bytes — the DRS blob is opaque; size is the only
	// thing the no-output branch reports.
	env := buildDRSReply(make([]byte, 480))
	d, _ := newFakeDeviceWithReply(env)
	var status bytes.Buffer
	if err := runGetDrum(&status, d, ""); err != nil {
		t.Fatalf("runGetDrum: %v", err)
	}
	if !strings.Contains(status.String(), "DRS: 480 bytes") {
		t.Errorf("expected size status, got %q", status.String())
	}
}

func TestRunGetDrum_WithOutputWritesFile(t *testing.T) {
	payload := make([]byte, 480)
	for i := range payload {
		payload[i] = byte(i)
	}
	env := buildDRSReply(payload)
	d, _ := newFakeDeviceWithReply(env)
	out := filepath.Join(t.TempDir(), "drs.bin")
	var status bytes.Buffer
	if err := runGetDrum(&status, d, out); err != nil {
		t.Fatalf("runGetDrum: %v", err)
	}
	disk, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read written drs: %v", err)
	}
	if len(disk) != 480 {
		t.Errorf("written file = %d bytes, want 480", len(disk))
	}
	if disk[100] != 100 {
		t.Errorf("written bytes don't match seed: disk[100]=%d", disk[100])
	}
	if !strings.Contains(status.String(), "wrote") {
		t.Errorf("expected 'wrote ...' status, got %q", status.String())
	}
}

func TestRunSetDrum_SendsEnvelopeWithExpectedShape(t *testing.T) {
	d, fake := newFakeDeviceWithReply(nil)
	bytesIn := make([]byte, 480)
	if err := runSetDrum(d, bytesIn); err != nil {
		t.Fatalf("runSetDrum: %v", err)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(fake.sent))
	}
	env := fake.sent[0]
	if env[3] != protocol.FuncDRS {
		t.Errorf("expected FuncDRS (%#x), got %#x", protocol.FuncDRS, env[3])
	}
}

func TestParseBaudRate_HappyPath(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"9600", 9600},
		{"38400", 38400},
		{"115200", 115200},
		{"300", 300},
	}
	for _, c := range cases {
		got, err := parseBaudRate(c.in)
		if err != nil {
			t.Errorf("parseBaudRate(%q): unexpected err %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseBaudRate(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseBaudRate_RejectsOutOfRange(t *testing.T) {
	for _, in := range []string{"299", "200", "115210"} {
		if _, err := parseBaudRate(in); err == nil {
			t.Errorf("parseBaudRate(%q) should reject out-of-range", in)
		}
	}
}

func TestParseBaudRate_RejectsNonMultipleOfTen(t *testing.T) {
	// RSBAUD stores rate/10 — non-multiples can't round-trip.
	for _, in := range []string{"9601", "38405", "1234"} {
		if _, err := parseBaudRate(in); err == nil {
			t.Errorf("parseBaudRate(%q) should reject non-multiple of 10", in)
		}
	}
}

func TestParseBaudRate_RejectsNonNumeric(t *testing.T) {
	for _, in := range []string{"", "abc", "fast"} {
		if _, err := parseBaudRate(in); err == nil {
			t.Errorf("parseBaudRate(%q) should reject non-numeric", in)
		}
	}
}

func TestRunSetBaud_PatchesRSBAUDInOVSWrite(t *testing.T) {
	// Seed the fake with a realistic OVS reply (current baud 19200).
	// runSetBaud reads it, patches the RSBAUD field, writes back.
	// The captured outbound envelope must carry the new rate/10.
	current := &protocol.OverallSettings{ProgName: "X", BaudRate: 19200}
	env, err := buildOVSReply(current)
	if err != nil {
		t.Fatalf("buildOVSReply: %v", err)
	}
	d, fake := newFakeDeviceWithReply(env)
	var status bytes.Buffer
	if err := runSetBaud(&status, d, 76800); err != nil {
		t.Fatalf("runSetBaud: %v", err)
	}
	// Two messages on the wire: the ROVS request + the patched OVS write.
	if len(fake.sent) != 2 {
		t.Fatalf("expected 2 sends (ROVS + OVS write), got %d", len(fake.sent))
	}
	write := fake.sent[1]
	// Parse the written envelope, fish out the RSBAUD field, confirm patched.
	akai, err := protocol.ParseAkai(write)
	if err != nil {
		t.Fatalf("written envelope not AKAI-parseable: %v", err)
	}
	if akai.Function != protocol.FuncOVS {
		t.Errorf("function = %d, want FuncOVS", akai.Function)
	}
	settings, err := protocol.ParseOverallSettings(akai.Payload)
	if err != nil {
		t.Fatalf("ParseOverallSettings on written payload: %v", err)
	}
	if settings.BaudRate != 76800 {
		t.Errorf("patched BaudRate = %d, want 76800", settings.BaudRate)
	}
	// Status must include both the "current" and "changing to" lines.
	if !strings.Contains(status.String(), "19200") {
		t.Errorf("status should report previous baud, got %q", status.String())
	}
	if !strings.Contains(status.String(), "76800") {
		t.Errorf("status should report new baud, got %q", status.String())
	}
}
