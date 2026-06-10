package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
)

// Tests for the uploadSample flow and its extracted pure helpers:
// prepareSampleAudio (file load + resample + truncate + convert)
// and applyTuneToPitch (SNOMP clamping math). The uploadSample test
// drives the full wire orchestration with a queuedReplyTransport so
// the multi-step sequence (PickSlot → SDATA → CollectNAKs → SPRM
// round-trip) can be exercised end-to-end without hardware.

// queuedReplyTransport returns canned replies in order. A nil entry
// in the queue is a sentinel for "simulated timeout" — required for
// uploadSample's flow where CollectNAKs needs to see an immediate
// timeout (no NAKs) between the SDATA upload and the GetParams read.
// Without the sentinel CollectNAKs would consume the SPRM reply meant
// for GetParams.
type queuedReplyTransport struct {
	sent    [][]byte
	replies [][]byte
	cursor  int
}

func (q *queuedReplyTransport) Send(b []byte) error {
	cp := make([]byte, len(b))
	copy(cp, b)
	q.sent = append(q.sent, cp)
	return nil
}
func (q *queuedReplyTransport) RecvSysEx(_ time.Duration) ([]byte, error) {
	if q.cursor >= len(q.replies) {
		return nil, errors.New("simulated timeout")
	}
	r := q.replies[q.cursor]
	q.cursor++
	if r == nil {
		return nil, errors.New("simulated timeout")
	}
	return r, nil
}
func (q *queuedReplyTransport) Drain()          {}
func (q *queuedReplyTransport) Close() error    { return nil }
func (q *queuedReplyTransport) InName() string  { return "fake" }
func (q *queuedReplyTransport) OutName() string { return "fake" }

// makeUploadTestWAV writes a small mono 16-bit WAV at `rate` Hz with
// `frames` samples. Used to feed prepareSampleAudio / uploadSample
// without an external fixture file.
func makeUploadTestWAV(t *testing.T, rate, frames int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.wav")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test wav: %v", err)
	}
	defer f.Close()
	enc := wav.NewEncoder(f, rate, 16, 1, 1)
	data := make([]int, frames)
	for i := range data {
		// Ramp through 0 then back; deterministic and non-silent.
		data[i] = (i * 200) - (frames * 100)
	}
	buf := &audio.IntBuffer{
		Format:         &audio.Format{NumChannels: 1, SampleRate: rate},
		SourceBitDepth: 16,
		Data:           data,
	}
	if err := enc.Write(buf); err != nil {
		t.Fatalf("encode test wav: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("close test wav: %v", err)
	}
	return path
}

// emptyCATEnvelope returns a 0-entry CAT envelope so PickSlot picks
// the preferred slot unchanged (every slot reads as free).
func emptyCATEnvelope() []byte {
	return protocol.BuildAkaiData(0, protocol.FuncCAT, 0, nil)
}

// spramEnvelope returns a synthesized SPRM envelope encoding `p`.
// Used to seed a queuedReplyTransport's reply for GetParams after
// the SDATA upload.
func sprmEnvelope(p *protocol.SampleParams) []byte {
	return protocol.BuildAkaiData(0, protocol.FuncSPRM, 0, p.EncodePayload())
}

func TestApplyTuneToPitch_PositiveSemitonesLowersSnomp(t *testing.T) {
	// +12 semitones (one octave up) → SNOMP drops by 12*16 = 192.
	// Per the dxzl direction: lower SNOMP plays higher at the same key.
	if got := applyTuneToPitch(960, 12); got != 960-192 {
		t.Errorf("applyTuneToPitch(960, +12) = %d, want %d", got, 960-192)
	}
}

func TestApplyTuneToPitch_NegativeSemitonesRaisesSnomp(t *testing.T) {
	if got := applyTuneToPitch(960, -6); got != 960+96 {
		t.Errorf("applyTuneToPitch(960, -6) = %d, want %d", got, 960+96)
	}
}

func TestApplyTuneToPitch_ClampsBelowZero(t *testing.T) {
	// Tuning +60 semitones (5 octaves up) from SNOMP=100: would underflow.
	if got := applyTuneToPitch(100, 60); got != 0 {
		t.Errorf("applyTuneToPitch(100, +60) = %d, want 0 (clamped)", got)
	}
}

func TestApplyTuneToPitch_ClampsAboveUint16(t *testing.T) {
	// Tuning -10000 semitones from SNOMP=100: would overflow.
	if got := applyTuneToPitch(100, -10000); got != 0xFFFF {
		t.Errorf("applyTuneToPitch(100, -10000) = %d, want 0xFFFF (clamped)", got)
	}
}

func TestPrepareSampleAudio_HappyPath_InRangeWAV(t *testing.T) {
	path := makeUploadTestWAV(t, 22050, 500)
	var status bytes.Buffer
	a, words, err := prepareSampleAudio(&status, uploadSampleOpts{Path: path})
	if err != nil {
		t.Fatalf("prepareSampleAudio: %v", err)
	}
	// 22050 Hz is in-range → no resample. words should match PCM 1:1.
	if a.SampleRate != 22050 {
		t.Errorf("SampleRate = %d, want 22050 (in-range source)", a.SampleRate)
	}
	if len(words) != len(a.PCM) {
		t.Errorf("len(words) %d != len(PCM) %d", len(words), len(a.PCM))
	}
	// Status reports the loaded frame count.
	if !strings.Contains(status.String(), "loaded") || !strings.Contains(status.String(), "22050Hz") {
		t.Errorf("status missing 'loaded ... 22050Hz', got %q", status.String())
	}
}

func TestPrepareSampleAudio_ResamplesWhenRateSpecified(t *testing.T) {
	// Source @ 22050 → ask for 11025 → status shows the resample line.
	path := makeUploadTestWAV(t, 22050, 1000)
	var status bytes.Buffer
	a, _, err := prepareSampleAudio(&status, uploadSampleOpts{
		Path: path,
		Rate: "11025",
	})
	if err != nil {
		t.Fatalf("prepareSampleAudio: %v", err)
	}
	if a.SampleRate != 11025 {
		t.Errorf("SampleRate = %d, want 11025 (after resample)", a.SampleRate)
	}
	if !strings.Contains(status.String(), "resampled 22050Hz -> 11025Hz") {
		t.Errorf("expected resample status line, got %q", status.String())
	}
}

func TestPrepareSampleAudio_RejectsRateOutOfS950Range(t *testing.T) {
	path := makeUploadTestWAV(t, 22050, 500)
	var status bytes.Buffer
	_, _, err := prepareSampleAudio(&status, uploadSampleOpts{
		Path: path,
		Rate: "100", // way below MinSampleRateHz
	})
	if err == nil {
		t.Fatal("expected error when requested rate is out of S950 range")
	}
	if !strings.Contains(err.Error(), "outside S950 range") {
		t.Errorf("error should mention 'outside S950 range'; got %v", err)
	}
}

func TestPrepareSampleAudio_TruncatesToMaxFrames(t *testing.T) {
	path := makeUploadTestWAV(t, 22050, 1000)
	var status bytes.Buffer
	_, words, err := prepareSampleAudio(&status, uploadSampleOpts{
		Path:      path,
		MaxFrames: 300,
	})
	if err != nil {
		t.Fatalf("prepareSampleAudio: %v", err)
	}
	if len(words) != 300 {
		t.Errorf("len(words) = %d, want 300 (truncated)", len(words))
	}
	if !strings.Contains(status.String(), "truncated to 300 frames") {
		t.Errorf("expected truncated status, got %q", status.String())
	}
}

func TestPrepareSampleAudio_ErrorsOnMissingFile(t *testing.T) {
	_, _, err := prepareSampleAudio(&bytes.Buffer{}, uploadSampleOpts{
		Path: "/nonexistent/sample.wav",
	})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestUploadSample_HappyPath_NoSPRMPatchNeeded(t *testing.T) {
	// 1000-frame WAV @ 22050 → 1000 12-bit words. No name/tune/loop
	// override means needsSPRM == false: PickSlot reads catalog,
	// PutSampleOpenLoop sends SDATA, CollectNAKs finds nothing, done.
	// Queue: just the empty CAT reply for PickSlot.
	path := makeUploadTestWAV(t, 22050, 1000)
	fake := &queuedReplyTransport{replies: [][]byte{emptyCATEnvelope()}}
	d := device.New(fake, 0)

	var status bytes.Buffer
	slot, err := uploadSample(&status, d, uploadSampleOpts{
		Path:          path,
		PreferredSlot: 5,
	})
	if err != nil {
		t.Fatalf("uploadSample: %v", err)
	}
	if slot != 5 {
		t.Errorf("returned slot = %d, want 5 (preferred, free)", slot)
	}
	// 2 sends on the wire: RCAT (PickSlot) + the big SDATA upload.
	if len(fake.sent) != 2 {
		t.Errorf("expected 2 sends (RCAT + SDATA), got %d", len(fake.sent))
	}
	// First send is the RCAT request (8 bytes, ends F7).
	rcat := fake.sent[0]
	if rcat[0] != 0xF0 || rcat[1] != 0x47 || rcat[3] != protocol.FuncRCAT {
		t.Errorf("first send doesn't look like RCAT: % X", rcat[:5])
	}
	// Second send is the SDATA dump — must start F0 7E 01 (universal NRT, SD).
	sdata := fake.sent[1]
	if sdata[0] != 0xF0 || sdata[1] != protocol.UniversalNRT || sdata[2] != protocol.CodeSD {
		t.Errorf("second send doesn't look like SDATA: % X", sdata[:5])
	}
	// Status should show the load + queued bytes lines.
	for _, want := range []string{"loaded", "22050Hz", "queued", "drain"} {
		if !strings.Contains(status.String(), want) {
			t.Errorf("expected %q in status, got %q", want, status.String())
		}
	}
}

func TestUploadSample_WithSPRMPatch_WritesNameAndPitch(t *testing.T) {
	// needsSPRM = true (Name + TuneSemitones). Queue: CAT for PickSlot,
	// then SPRM for GetParams after the upload. The final SetParams
	// send must contain a payload that re-parses to a SampleParams
	// with Name = "TUNED" and pitch shifted by -12 semitones * 16.
	path := makeUploadTestWAV(t, 22050, 800)

	// Seed the initial SPRM the device "returns" from GetParams.
	initial := &protocol.SampleParams{
		Name:         "OLDNAME",
		TotalWords:   800,
		SampleRateHz: 22050,
		NominalPitch: 960, // C3 default
		ReplayMode:   'O',
		Reversed:     'N',
	}
	// Queue order: CAT (for PickSlot's Catalog), timeout sentinel
	// (so CollectNAKs sees no NAKs and doesn't accidentally consume
	// the SPRM), then SPRM (for GetParams after the upload).
	fake := &queuedReplyTransport{
		replies: [][]byte{
			emptyCATEnvelope(),
			nil,
			sprmEnvelope(initial),
		},
	}
	d := device.New(fake, 0)

	var status bytes.Buffer
	_, err := uploadSample(&status, d, uploadSampleOpts{
		Path:          path,
		Name:          "TUNED",
		TuneSemitones: 12, // +12 semitones up
		PreferredSlot: 7,
	})
	if err != nil {
		t.Fatalf("uploadSample: %v", err)
	}
	// 4 sends: RCAT (PickSlot), SDATA, RSPRM (GetParams), SPRM (SetParams).
	if len(fake.sent) != 4 {
		t.Fatalf("expected 4 sends (RCAT/SDATA/RSPRM/SPRM), got %d", len(fake.sent))
	}
	// Last send is the SPRM write. Parse and verify the patched fields.
	akai, perr := protocol.ParseAkai(fake.sent[3])
	if perr != nil {
		t.Fatalf("SPRM write not AKAI-parseable: %v", perr)
	}
	if akai.Function != protocol.FuncSPRM {
		t.Errorf("last send function = %d, want FuncSPRM", akai.Function)
	}
	got, perr := protocol.ParseSampleParams(akai.Payload)
	if perr != nil {
		t.Fatalf("ParseSampleParams on written SPRM: %v", perr)
	}
	// Name padded with trailing spaces on encode — trim for comparison.
	if name := strings.TrimRight(got.Name, " "); name != "TUNED" {
		t.Errorf("written Name = %q, want %q", name, "TUNED")
	}
	if got.NominalPitch != 960-192 {
		t.Errorf("written NominalPitch = %d, want %d (= 960 - 12*16)",
			got.NominalPitch, 960-192)
	}
}

func TestUploadSample_PropagatesPrepareError(t *testing.T) {
	// Bad path → prepareSampleAudio errors → uploadSample returns
	// without any wire traffic.
	fake := &queuedReplyTransport{}
	d := device.New(fake, 0)
	_, err := uploadSample(&bytes.Buffer{}, d, uploadSampleOpts{
		Path: "/no/such/file.wav",
	})
	if err == nil {
		t.Fatal("expected error from missing file")
	}
	if len(fake.sent) != 0 {
		t.Errorf("no wire traffic should happen on prepare failure, got %d sends", len(fake.sent))
	}
}
