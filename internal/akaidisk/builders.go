// Builders that turn the project's protocol types into S900/S950
// disk files. The key format fact making these thin: every disk
// file payload is the DB-decoded form of the corresponding SysEx
// data payload (each RAM byte travels as two 7-bit wire bytes), so
// the protocol package's existing encoders do all the field work
// and these helpers just strip the wire encoding.

package akaidisk

import (
	"fmt"

	"github.com/bivers/s950/internal/protocol"
)

// BuildSampleFile produces a complete 'S' file: the 60-byte RAM
// sample header (DB-decoded SPRM) followed by the 12-bit packed
// audio. The SPRM's TotalWords must match len(words) — the header
// is the loader's source of truth for the sample length.
func BuildSampleFile(p *protocol.SampleParams, words []uint16) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("akaidisk: nil sample params")
	}
	if int(p.TotalWords) != len(words) {
		return nil, fmt.Errorf("akaidisk: SPRM TotalWords %d != %d words supplied — the S950's loader trusts the header",
			p.TotalWords, len(words))
	}
	hdr := DecodeDBPayload(p.EncodePayload())
	return append(hdr, PackSamples(words)...), nil
}

// BuildProgramFile produces a 'P' file: the 38-byte RAM program
// header plus one 70-byte block per keygroup (DB-decoded PRGM
// payload).
func BuildProgramFile(p *protocol.Program) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("akaidisk: nil program")
	}
	return DecodeDBPayload(p.EncodePayload()), nil
}

// BuildOverallFile produces the 40-byte 'O' file (DB-decoded OVS
// block). forceRS232 flips the controller-select byte to RS-232C in
// the written file — the whole point of shipping an OVERALL SE on
// exported images: hardware ignores SysEx writes to that field, but
// a disk auto-loaded at power-on may restore it (pending the
// hardware test recorded in the project notes), letting the sampler
// boot RS-232-ready.
func BuildOverallFile(o *protocol.OverallSettings, forceRS232 bool) ([]byte, error) {
	if o == nil {
		return nil, fmt.Errorf("akaidisk: nil overall settings")
	}
	wire, err := o.EncodePayload()
	if err != nil {
		return nil, fmt.Errorf("akaidisk: encode OVS: %w", err)
	}
	raw := DecodeDBPayload(wire)
	if forceRS232 {
		// ctrlport lives at RAM byte 29 (wire offset 65/66 per the
		// dxzl M1RS2 define, (65-7)/2 = 29): 1 = MIDI, 2 = RS-232C.
		const ctrlportOff = 29
		if len(raw) <= ctrlportOff {
			return nil, fmt.Errorf("akaidisk: OVS payload too short for ctrlport patch (%d bytes)", len(raw))
		}
		raw[ctrlportOff] = 2
	}
	return raw, nil
}
