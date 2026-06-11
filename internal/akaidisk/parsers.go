// Read-direction converters: disk files back into the project's
// protocol types. Mirrors builders.go — disk payloads are DB-decoded
// SysEx payloads, so parsing is "re-encode to wire form, hand to the
// protocol package's existing parsers". One image format, one set of
// field codecs.
//
// Together with builders.go this makes a Gotek image the app's
// native patch format: a single file that round-trips programs +
// samples + settings through the app AND boots the real sampler.

package akaidisk

import (
	"fmt"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/sysex"
)

// sampleHeaderLen is the on-disk S900 sample header size.
const sampleHeaderLen = 60

// programHeaderLen / keygroupLen are the on-disk program sizes
// (wire sizes halved).
const (
	programHeaderLen = 38
	keygroupLen      = 70
)

// overallLen is the on-disk OVERALL SE size.
const overallLen = 40

// EncodeDBPayload converts raw RAM bytes into the DB wire encoding
// (two 7-bit bytes per RAM byte) — the inverse of DecodeDBPayload.
func EncodeDBPayload(raw []byte) []byte {
	out := make([]byte, 2*len(raw))
	for i, b := range raw {
		lo, hi := sysex.EncodeDB(b)
		out[2*i] = lo
		out[2*i+1] = hi
	}
	return out
}

// ParseSampleFile decodes an 'S' file into its SPRM and 12-bit
// words. Only the non-compressed sample format is supported — check
// Entry.Compressed before calling (compressed files are rare in
// practice; every file on the user's real library disk was
// non-compressed).
func ParseSampleFile(f []byte) (*protocol.SampleParams, []uint16, error) {
	if len(f) < sampleHeaderLen {
		return nil, nil, fmt.Errorf("akaidisk: sample file too short (%d bytes)", len(f))
	}
	p, err := protocol.ParseSampleParams(EncodeDBPayload(f[:sampleHeaderLen]))
	if err != nil {
		return nil, nil, fmt.Errorf("akaidisk: sample header: %w", err)
	}
	words, err := UnpackSamples(f[sampleHeaderLen:], int(p.TotalWords))
	if err != nil {
		return nil, nil, fmt.Errorf("akaidisk: sample %q: %w", p.Name, err)
	}
	return p, words, nil
}

// ParseProgramFile decodes a 'P' file into a Program.
func ParseProgramFile(f []byte) (*protocol.Program, error) {
	if len(f) < programHeaderLen {
		return nil, fmt.Errorf("akaidisk: program file too short (%d bytes)", len(f))
	}
	if (len(f)-programHeaderLen)%keygroupLen != 0 {
		return nil, fmt.Errorf("akaidisk: program file size %d is not header + N keygroups", len(f))
	}
	p, err := protocol.ParseProgram(EncodeDBPayload(f))
	if err != nil {
		return nil, fmt.Errorf("akaidisk: program: %w", err)
	}
	return p, nil
}

// ParseOverallFile decodes an 'O' file.
func ParseOverallFile(f []byte) (*protocol.OverallSettings, error) {
	if len(f) != overallLen {
		return nil, fmt.Errorf("akaidisk: OVERALL SE is %d bytes, want %d", len(f), overallLen)
	}
	o, err := protocol.ParseOverallSettings(EncodeDBPayload(f))
	if err != nil {
		return nil, fmt.Errorf("akaidisk: overall settings: %w", err)
	}
	return o, nil
}
