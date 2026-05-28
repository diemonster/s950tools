// Overall settings (OVS / func 8) — device-wide configuration the
// S950's "MIDI" menu controls. Byte offsets derived from
// dxzl/akai-s950's OverallSettingsForm.h (the only public reference
// that names the field positions). Verified by round-tripping the
// device's actual OVS reply during development.
//
// Wire layout: F0 47 <chan> 08 40 00 00 <80 data bytes> <checksum> F7
//
// The data bytes use the Akai DB/DW codec (see sysex/codec.go). The
// envelope offset → payload offset shift is -7 (the 7-byte AKAI
// header), so this file works in payload coordinates: payload[0]
// = envelope[7] = PRONAME.

package protocol

import (
	"errors"
	"fmt"

	"github.com/bivers/s950/internal/sysex"
)

// OVSPayloadSize is the data byte count between the AKAI header and
// the trailing checksum + F7. Matches `OSSIZ - 7 - 2 = 80` from
// dxzl's defines.
const OVSPayloadSize = 80

// Payload-relative offsets — envelope offsets minus the 7-byte
// AKAI header. The DB / DW comments document the codec each field
// uses; widths match the codec's wire size.
const (
	ovsOffPRONAME  = 7 - 7  // 0  : DB × 10  → 20 wire bytes
	ovsOffMDXTCH   = 39 - 7 // 32 : DB
	ovsOffRSCHNL   = 47 - 7 // 40 : DW
	ovsOffRSKEY    = 51 - 7 // 44 : DW
	ovsOffRSVEL    = 55 - 7 // 48 : DW
	ovsOffBASMCH   = 61 - 7 // 54 : DB (high bit = omni)
	ovsOffMLEN     = 63 - 7 // 56 : DB (0/1 — loudness on CC7)
	ovsOffM1RS2    = 65 - 7 // 58 : DB (1=MIDI, 2=RS-232 — read-only on the wire)
	ovsOffMPEN     = 67 - 7 // 60 : DB (0=disable, non-zero=enable)
	ovsOffPWRANGE  = 77 - 7 // 70 : DB (0..12 semitones)
	ovsOffRSBAUD   = 79 - 7 // 72 : DW (baud / 10)
	// OSCONST1 / OSCONST2 (envelope offsets 69 / 73) are reserved
	// constants — the device expects 20727 and 7238 respectively.
	// We don't expose them as fields but ParseOverallSettings
	// preserves them inside Raw so encode round-trips cleanly.
)

// OverallSettings is the high-level decoded view of the 80-byte
// OVS payload. Field names match the device-side mnemonics (the
// S950 manual uses these in its MIDI menu pages).
type OverallSettings struct {
	// ProgName is the default program name shown on the front panel.
	// 10 ASCII chars, space-padded.
	ProgName string
	// MidiTxChannel is the channel used when the device emits
	// AKAI-exclusive replies. Distinct from BasicChannel (which is
	// what the device LISTENS on). 0..15.
	MidiTxChannel uint8
	// RxSimChannel / RxSimKey / RxSimVelocity drive the front-panel
	// reception simulator (used to test programs without an
	// external controller). Channel 1..16, key 0..127, vel 0..127.
	RxSimChannel  uint16
	RxSimKey      uint16
	RxSimVelocity uint16
	// BasicChannel is the device's MIDI receive channel (0..15).
	BasicChannel uint8
	// OmniOn = true means the device responds on ALL channels,
	// ignoring BasicChannel. Encoded as bit 7 of the BASMCH byte.
	OmniOn bool
	// LoudnessOnCC7 enables MIDI continuous-controller 7 as a
	// loudness modulator. Stored as 0/1 on the wire.
	LoudnessOnCC7 bool
	// ControllerSelect is 1 = MIDI, 2 = RS-232C. The S950 firmware
	// SILENTLY IGNORES writes to this field — it's effectively a
	// read-only status indicator over the wire. Use the front-panel
	// MIDI menu to flip controller mode. See
	// project-ovs-write-restriction for the discovery context.
	ControllerSelect uint8
	// MPEEnabled toggles MIDI Polyphonic Expression mode. Stored as
	// 0 = disable, non-zero = enable on the wire.
	MPEEnabled bool
	// PitchWheelRange is the bender's ± range in semitones (0..12).
	PitchWheelRange uint8
	// BaudRate is the RS-232 line rate (300..115200, in plain Hz).
	// Stored on the wire as BaudRate/10 (a 16-bit value, so the
	// max representable rate is 655350; the front panel caps it
	// at 115200). See project-pending-work for the 50000 ceiling
	// on firmware 1.2a.
	BaudRate uint32
	// Raw is the 80-byte payload the device handed us, kept so an
	// Encode round-trip preserves OSCONST1/OSCONST2 and any other
	// reserved bytes we don't model. The first SetOverall after a
	// GetOverall uses Raw as the base and only overwrites the
	// fields we modelled — exactly mirroring SampleParams.
	Raw [OVSPayloadSize]byte
}

// ParseOverallSettings decodes an 80-byte OVS data payload (the
// `AkaiMessage.Payload` shape — NOT the full envelope). The caller
// is responsible for checksum validation; ParseAkai does that
// upstream.
func ParseOverallSettings(payload []byte) (*OverallSettings, error) {
	if len(payload) != OVSPayloadSize {
		return nil, fmt.Errorf("OVS payload is %d bytes, want %d", len(payload), OVSPayloadSize)
	}
	o := &OverallSettings{}
	copy(o.Raw[:], payload)

	// PRONAME: 10 DB groups → 10 ASCII chars.
	name := make([]byte, 10)
	for i := 0; i < 10; i++ {
		name[i] = sysex.DecodeDB(payload[ovsOffPRONAME+i*2], payload[ovsOffPRONAME+i*2+1])
	}
	o.ProgName = string(name)

	o.MidiTxChannel = sysex.DecodeDB(payload[ovsOffMDXTCH], payload[ovsOffMDXTCH+1])
	o.RxSimChannel = sysex.DecodeDW([4]byte{
		payload[ovsOffRSCHNL], payload[ovsOffRSCHNL+1],
		payload[ovsOffRSCHNL+2], payload[ovsOffRSCHNL+3],
	})
	o.RxSimKey = sysex.DecodeDW([4]byte{
		payload[ovsOffRSKEY], payload[ovsOffRSKEY+1],
		payload[ovsOffRSKEY+2], payload[ovsOffRSKEY+3],
	})
	o.RxSimVelocity = sysex.DecodeDW([4]byte{
		payload[ovsOffRSVEL], payload[ovsOffRSVEL+1],
		payload[ovsOffRSVEL+2], payload[ovsOffRSVEL+3],
	})

	basmch := sysex.DecodeDB(payload[ovsOffBASMCH], payload[ovsOffBASMCH+1])
	o.BasicChannel = basmch & 0x7F
	o.OmniOn = basmch&0x80 != 0

	o.LoudnessOnCC7 = sysex.DecodeDB(payload[ovsOffMLEN], payload[ovsOffMLEN+1]) != 0
	o.ControllerSelect = sysex.DecodeDB(payload[ovsOffM1RS2], payload[ovsOffM1RS2+1])
	o.MPEEnabled = sysex.DecodeDB(payload[ovsOffMPEN], payload[ovsOffMPEN+1]) != 0
	o.PitchWheelRange = sysex.DecodeDB(payload[ovsOffPWRANGE], payload[ovsOffPWRANGE+1])

	baudDiv10 := sysex.DecodeDW([4]byte{
		payload[ovsOffRSBAUD], payload[ovsOffRSBAUD+1],
		payload[ovsOffRSBAUD+2], payload[ovsOffRSBAUD+3],
	})
	o.BaudRate = uint32(baudDiv10) * 10

	return o, nil
}

// EncodePayload re-emits the 80-byte payload, starting from Raw
// (so OSCONST1/OSCONST2 and any reserved bytes round-trip) and
// overwriting the high-level fields. Mirrors SampleParams.EncodePayload.
//
// Returns an error if any field is out of its documented range —
// catching bad data before it hits the wire avoids the device
// silently rejecting an entire OVS write because one byte was off.
func (o *OverallSettings) EncodePayload() ([]byte, error) {
	if o.MidiTxChannel > 15 {
		return nil, fmt.Errorf("MidiTxChannel %d out of range (0..15)", o.MidiTxChannel)
	}
	if o.BasicChannel > 15 {
		return nil, fmt.Errorf("BasicChannel %d out of range (0..15)", o.BasicChannel)
	}
	if o.PitchWheelRange > 12 {
		return nil, fmt.Errorf("PitchWheelRange %d out of range (0..12)", o.PitchWheelRange)
	}
	if o.BaudRate%10 != 0 {
		return nil, fmt.Errorf("BaudRate %d must be a multiple of 10 (RSBAUD stores rate/10)", o.BaudRate)
	}
	if o.BaudRate/10 > 0xFFFF {
		return nil, fmt.Errorf("BaudRate %d exceeds the 16-bit RSBAUD field (max %d)", o.BaudRate, 0xFFFF*10)
	}

	out := o.Raw
	// PRONAME — 10 chars, space-padded / truncated to fit.
	name := o.ProgName
	for i := 0; i < 10; i++ {
		var c byte = ' '
		if i < len(name) {
			c = name[i]
		}
		lo, hi := sysex.EncodeDB(c)
		out[ovsOffPRONAME+i*2] = lo
		out[ovsOffPRONAME+i*2+1] = hi
	}

	writeDB(out[:], ovsOffMDXTCH, o.MidiTxChannel)
	writeDW(out[:], ovsOffRSCHNL, o.RxSimChannel)
	writeDW(out[:], ovsOffRSKEY, o.RxSimKey)
	writeDW(out[:], ovsOffRSVEL, o.RxSimVelocity)

	basmch := o.BasicChannel & 0x7F
	if o.OmniOn {
		basmch |= 0x80
	}
	writeDB(out[:], ovsOffBASMCH, basmch)
	writeDB(out[:], ovsOffMLEN, boolByte(o.LoudnessOnCC7))
	writeDB(out[:], ovsOffM1RS2, o.ControllerSelect)
	writeDB(out[:], ovsOffMPEN, boolByte(o.MPEEnabled))
	writeDB(out[:], ovsOffPWRANGE, o.PitchWheelRange)
	writeDW(out[:], ovsOffRSBAUD, uint16(o.BaudRate/10))

	return out[:], nil
}

// writeDB / writeDW are tiny inline helpers — encode-then-copy is
// the typical pattern but doing it via local helpers makes the
// field list read more like a manifest than a sequence of indexed
// pokes.
func writeDB(buf []byte, off int, v byte) {
	lo, hi := sysex.EncodeDB(v)
	buf[off] = lo
	buf[off+1] = hi
}
func writeDW(buf []byte, off int, v uint16) {
	enc := sysex.EncodeDW(v)
	copy(buf[off:off+4], enc[:])
}
func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// ErrInvalidOVS is returned when the OVS payload looks structurally
// wrong (e.g. ParseAkai handed us something that isn't an OVS data
// message). The Wails layer surfaces it to the frontend.
var ErrInvalidOVS = errors.New("not an OVS message")
