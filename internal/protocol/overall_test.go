package protocol

import (
	"testing"
)

// buildOVSPayload constructs a realistic 80-byte OVS payload from
// known-good wire bytes captured during RS-232 development. Used as
// a fixture by both the ParseOverallSettings happy-path and the
// EncodePayload round-trip test.
func buildOVSPayload() [OVSPayloadSize]byte {
	// Start from a real device dump (RX 89 bytes during the
	// OVS-write experiment): "DEFAULT PR" name, channel 0 omni
	// on, M1RS2 = 2 (RS-232C), baud = 9600 → RSBAUD 960.
	o := OverallSettings{
		ProgName:         "DEFAULT PR",
		MidiTxChannel:    0,
		RxSimChannel:     1,
		RxSimKey:         60,
		RxSimVelocity:    64,
		BasicChannel:     0,
		OmniOn:           true,
		LoudnessOnCC7:    false,
		ControllerSelect: 2, // RS-232C
		MPEEnabled:       false,
		PitchWheelRange:  7,
		BaudRate:         9600,
	}
	// Seed Raw with the two OSCONST constants so EncodePayload
	// preserves them through a round-trip even though our model
	// doesn't expose them as fields.
	// OSCONST1 (envelope 69 / payload 62, DW): 20727
	writeDW(o.Raw[:], ovsOffRSBAUD-10 /* OSCONST1 = 62 */, 20727)
	writeDW(o.Raw[:], ovsOffRSBAUD-6 /* OSCONST2 = 66 */, 7238)
	enc, _ := o.EncodePayload()
	var out [OVSPayloadSize]byte
	copy(out[:], enc)
	return out
}

func TestOverallSettings_ParseHappyPath(t *testing.T) {
	payload := buildOVSPayload()
	o, err := ParseOverallSettings(payload[:])
	if err != nil {
		t.Fatalf("ParseOverallSettings: %v", err)
	}
	if o.ProgName != "DEFAULT PR" {
		t.Errorf("ProgName = %q, want %q", o.ProgName, "DEFAULT PR")
	}
	if o.ControllerSelect != 2 {
		t.Errorf("ControllerSelect = %d, want 2 (RS-232)", o.ControllerSelect)
	}
	if !o.OmniOn {
		t.Error("OmniOn should be true")
	}
	if o.PitchWheelRange != 7 {
		t.Errorf("PitchWheelRange = %d, want 7", o.PitchWheelRange)
	}
	if o.BaudRate != 9600 {
		t.Errorf("BaudRate = %d, want 9600", o.BaudRate)
	}
}

func TestOverallSettings_RoundTrip(t *testing.T) {
	// Decode then re-encode — bytes should match exactly. Catches
	// off-by-ones in the field offset constants AND any bit-twiddling
	// asymmetry between DecodeDB/EncodeDB.
	original := buildOVSPayload()
	o, err := ParseOverallSettings(original[:])
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	encoded, err := o.EncodePayload()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for i, b := range original {
		if encoded[i] != b {
			t.Errorf("byte %d: got 0x%02X, want 0x%02X", i, encoded[i], b)
		}
	}
}

func TestOverallSettings_EditedFieldsRoundTrip(t *testing.T) {
	// Modify the high-level fields, encode, re-parse, verify the
	// edits survive. Confirms each field maps to the right offset
	// independently of the others.
	original := buildOVSPayload()
	o, _ := ParseOverallSettings(original[:])

	o.ProgName = "NEWNAME   "
	o.BasicChannel = 5
	o.OmniOn = false
	o.LoudnessOnCC7 = true
	o.MPEEnabled = true
	o.PitchWheelRange = 12
	o.BaudRate = 38400
	o.RxSimChannel = 10
	o.RxSimKey = 72
	o.RxSimVelocity = 100
	o.MidiTxChannel = 3

	enc, err := o.EncodePayload()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := ParseOverallSettings(enc)
	if err != nil {
		t.Fatalf("Reparse: %v", err)
	}
	if back.ProgName != "NEWNAME   " {
		t.Errorf("ProgName = %q", back.ProgName)
	}
	if back.BasicChannel != 5 || back.OmniOn {
		t.Errorf("BASMCH round-trip: ch=%d omni=%v", back.BasicChannel, back.OmniOn)
	}
	if !back.LoudnessOnCC7 {
		t.Error("LoudnessOnCC7 should round-trip true")
	}
	if !back.MPEEnabled {
		t.Error("MPEEnabled should round-trip true")
	}
	if back.PitchWheelRange != 12 {
		t.Errorf("PitchWheelRange = %d, want 12", back.PitchWheelRange)
	}
	if back.BaudRate != 38400 {
		t.Errorf("BaudRate = %d, want 38400", back.BaudRate)
	}
	if back.RxSimChannel != 10 || back.RxSimKey != 72 || back.RxSimVelocity != 100 {
		t.Errorf("Rx-sim round-trip: ch=%d key=%d vel=%d",
			back.RxSimChannel, back.RxSimKey, back.RxSimVelocity)
	}
	if back.MidiTxChannel != 3 {
		t.Errorf("MidiTxChannel = %d, want 3", back.MidiTxChannel)
	}
}

func TestOverallSettings_RejectsBadLength(t *testing.T) {
	_, err := ParseOverallSettings(make([]byte, 79))
	if err == nil {
		t.Fatal("expected error on short payload")
	}
}

func TestOverallSettings_RejectsOutOfRangeBaud(t *testing.T) {
	o := OverallSettings{BaudRate: 12345} // not a multiple of 10
	if _, err := o.EncodePayload(); err == nil {
		t.Error("expected error on non-divisible baud")
	}
}

func TestOverallSettings_RejectsOutOfRangeChannel(t *testing.T) {
	o := OverallSettings{BasicChannel: 99}
	if _, err := o.EncodePayload(); err == nil {
		t.Error("expected error on out-of-range basic channel")
	}
}
