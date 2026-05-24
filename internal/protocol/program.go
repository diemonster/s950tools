package protocol

import (
	"errors"
	"fmt"

	"github.com/bivers/s950/internal/sysex"
)

// Program / Keygroup byte layouts derived from dxzl/akai-s950's PRGHEDR and
// KEYGROUP structs (src/ProgramsForm.h). All offsets below are WIRE offsets
// within the PRGM payload — each "logical" byte takes 2 wire bytes (DB
// encoding); DW is 4 wire bytes (uint16); DD is 8 wire bytes (uint32).
//
// Program header: 76 wire bytes.
// Keygroup:       140 wire bytes each.
// One PRGM payload: header + N*keygroup, where 1 ≤ N ≤ 31.

const (
	ProgramHeaderSize = 76
	KeygroupSize      = 140
	MaxKeygroups      = 31
	NameWidth         = 10
)

// Program is the high-level decoded view of a PRGM payload.
type Program struct {
	Name              string     // 10 ASCII chars, trailing-space trimmed
	KeyTilt           int16      // -50..+50, key-vs-loudness scaling
	PositionalXFade   bool       // 0=off, 1=on
	NumKeygroups      uint8      // 1..31
	MidiProgramNumber uint8      // 0..127
	EnableMidiProgram bool       // S950 only; 0 disables, 255 enables
	Keygroups         []Keygroup // length matches NumKeygroups

	// RawHeader preserves the 76-byte wire payload of the program header.
	// On encode, RawHeader is used as the base and only known fields are
	// overwritten — so undocumented bytes that the S950 still uses round-trip
	// safely (same approach the SampleParams parser uses).
	RawHeader [ProgramHeaderSize]byte
}

// Keygroup is the high-level decoded view of one 140-byte keygroup block.
// Every named field has its name from dxzl's KEYGROUP struct; comments give
// the documented value ranges where they matter.
type Keygroup struct {
	// Key range and velocity routing.
	LowerKey       uint8 // 24..127 MIDI note
	UpperKey       uint8 // 24..127
	VelocitySwitch uint8 // 0..128 (above this, "loud" sample plays)

	// Amplitude envelope (0..99 unless noted).
	AttackTime   uint8
	DecayTime    uint8
	SustainLevel uint8
	ReleaseTime  uint8

	// Filter envelope (the filter's own ADSR, separate from amp).
	FilterAttackTime   uint8
	FilterDecayTime    uint8
	FilterSustainLevel uint8
	FilterReleaseTime  uint8

	// Filter/velocity routing.
	FilterVelInt        uint8 // amount velocity opens the filter
	FilterKeyTracking   uint8 // 0..99, 50=1V/oct equivalent
	AttackVelInt        uint8 // 0..99
	VelReleaseInt       int8  // signed, ±50 (stored 0..255)
	LoudnessVelInt      uint8 // 0..99
	PitchWarpVelInt     uint8
	PitchWarpOffset     int8 // ±50
	PitchWarpRecovery   uint8
	AdsrEnvToVcfFilter  int8  // ±50, how much amp ADSR modulates VCF
	AftertouchDepthMod  uint8 // 0..99
	ModWheelLfoDepthMod uint8 // 0..99

	// LFO.
	LfoBuildTime uint8 // 0..99
	LfoRate      uint8 // 0..99
	LfoDepth     uint8 // 0..99

	// Misc per-keygroup config.
	ControlBits     uint8 // bit flags: 0=transpose, 1=vel-xfade, 2=vibrato-desync,
	//                          // 3=one-shot trig, 4=vel-release-mode, 5=vel-xfade-curve
	VoiceOutAssign uint8 // 0=mono/0-9, 8=left group, 9=right group, 255=ALL
	MidiOffset     uint8 // 0..15
	VelXfade50pct  uint8 // 0..127 velocity-crossfade 50% point

	// "Soft" (lower-velocity) sample — the primary one.
	SoftSampleName   string  // 10 chars; the S950 finds the sample by name
	SoftTransposeRaw int16   // 1/16-semitone units, signed
	SoftFilter       uint8   // 0..99, 99=brightest
	SoftLoudness     int8    // ±50, .375dB per unit

	// "Loud" (higher-velocity) sample — alternative selected above VelocitySwitch.
	LoudSampleName   string
	LoudTransposeRaw int16
	LoudFilter       uint8
	LoudLoudness     int8

	// RawBytes preserves the 140-byte wire payload of this keygroup. Same
	// strategy as Program.RawHeader.
	RawBytes [KeygroupSize]byte
}

// Wire-byte offsets within the PRGM payload (program header section).
const (
	pHdrName      = 0  // 10 chars × DB = 20 wire bytes (0..19)
	pHdrUndef1    = 20 // DD (8 bytes)
	pHdrUndef2    = 28 // DW (4 bytes)
	pHdrKeyTilt   = 32 // DW signed (4 bytes)
	pHdrUndef3    = 36 // DW
	pHdrUndef4    = 40 // DB
	pHdrPosXFade  = 42 // DB
	pHdrReser1    = 44 // DB (=255)
	pHdrNumKgs    = 46 // DB
	pHdrUndef5    = 48 // DW
	pHdrMidiPgm   = 52 // DB
	pHdrEnableMP  = 54 // DB (255=enabled)
	pHdrReser2    = 56 // DW
	pHdrReser3    = 60 // DD
	pHdrReser4    = 68 // DD
)

// Wire-byte offsets within a keygroup block.
const (
	kgUMK       = 0   // upper MIDI key (DB)
	kgLMK       = 2   // lower MIDI key (DB)
	kgVST       = 4   // velocity switch threshold (DB)
	kgATK       = 6   // attack time (DB)
	kgDCY       = 8   // decay (DB)
	kgSSTN      = 10  // sustain level (DB)
	kgRLSE      = 12  // release time (DB)
	kgFVI       = 14  // filter vel int (DB)
	kgFKI       = 16  // filter key tracking (DB)
	kgAVI       = 18  // attack vel int (DB)
	kgRVI       = 20  // vel release int (signed, DB)
	kgLVI       = 22  // loudness vel int (DB)
	kgPVI       = 24  // pitch warp vel int (DB)
	kgPAO       = 26  // pitch warp offset (signed, DB)
	kgPST       = 28  // pitch warp recovery (DB)
	kgVBDLY     = 30  // LFO build time (DB)
	kgVBRATE    = 32  // LFO rate (DB)
	kgVBDPTH    = 34  // LFO depth (DB)
	kgKBITS     = 36  // control bits (DB)
	kgOPVOICE   = 38  // voice out assign (DB)
	kgKMDCHN    = 40  // MIDI offset (DB)
	kgAFDI      = 42  // aftertouch depth mod (DB)
	kgMWDI      = 44  // mod wheel LFO depth mod (DB)
	kgVCFAMNT   = 46  // ADSR→VCF (signed, DB)
	kgNAMEFS    = 48  // soft sample name (10 chars × DB = 20 wire bytes, 48..67)
	kgVCFAK     = 68  // filter ADSR attack (DB)
	kgVCFDY     = 70  // filter ADSR decay (DB)
	kgVCFST     = 72  // filter ADSR sustain (DB)
	kgVCFRL     = 74  // filter ADSR release (DB)
	kgVTMX      = 76  // vel xfade 50% (DB)
	// 78..83: reserved (KyUndef1 DB + KyUndef2 DW, kept in RawBytes)
	kgTROFFS    = 84  // soft sample transpose offset (signed, DW = 4 wire bytes)
	kgFLTFS     = 88  // soft sample filter (DB)
	kgLOUDFS    = 90  // soft sample loudness (signed, DB)
	kgNAMESS    = 92  // loud sample name (10 chars × DB = 20 wire bytes, 92..111)
	// 112..127: reserved (KyUndef3 DD + KyUndef4 DD)
	kgTROFSS    = 128 // loud sample transpose offset (signed, DW)
	kgFLTSS     = 132 // loud sample filter (DB)
	kgLOUDSS    = 134 // loud sample loudness (signed, DB)
	// 136..139: reserved (KyUndef5 DW)
)

// ParseProgram decodes a PRGM payload (header + 1..31 keygroups).
func ParseProgram(payload []byte) (*Program, error) {
	if len(payload) < ProgramHeaderSize+KeygroupSize {
		return nil, fmt.Errorf("PRGM payload too short: %d bytes (need at least %d)",
			len(payload), ProgramHeaderSize+KeygroupSize)
	}
	hdr := payload[:ProgramHeaderSize]
	body := payload[ProgramHeaderSize:]
	if len(body)%KeygroupSize != 0 {
		return nil, fmt.Errorf("PRGM body is %d bytes, not a multiple of keygroup size %d",
			len(body), KeygroupSize)
	}
	n := len(body) / KeygroupSize
	if n < 1 || n > MaxKeygroups {
		return nil, fmt.Errorf("PRGM has %d keygroups (want 1..%d)", n, MaxKeygroups)
	}

	p := &Program{
		Keygroups: make([]Keygroup, n),
	}
	copy(p.RawHeader[:], hdr)

	p.Name = sysex.DecodeName(*(*[20]byte)(hdr[pHdrName : pHdrName+20]))
	p.KeyTilt = int16(sysex.DecodeDW(*(*[4]byte)(hdr[pHdrKeyTilt : pHdrKeyTilt+4])))
	p.PositionalXFade = sysex.DecodeDB(hdr[pHdrPosXFade], hdr[pHdrPosXFade+1]) != 0
	p.NumKeygroups = sysex.DecodeDB(hdr[pHdrNumKgs], hdr[pHdrNumKgs+1])
	p.MidiProgramNumber = sysex.DecodeDB(hdr[pHdrMidiPgm], hdr[pHdrMidiPgm+1])
	p.EnableMidiProgram = sysex.DecodeDB(hdr[pHdrEnableMP], hdr[pHdrEnableMP+1]) != 0

	if int(p.NumKeygroups) != n {
		// Don't fail — the wire data is authoritative — but note the mismatch
		// so the user can spot it in JSON.
		p.NumKeygroups = uint8(n)
	}

	for i := 0; i < n; i++ {
		kg, err := parseKeygroup(body[i*KeygroupSize : (i+1)*KeygroupSize])
		if err != nil {
			return nil, fmt.Errorf("keygroup %d: %w", i, err)
		}
		p.Keygroups[i] = *kg
	}
	return p, nil
}

// NewDefaultProgram returns a Program initialized with safe defaults the
// S950 has been observed to accept: the field values match dxzl/akai-s950's
// commented example (the DEFAULT PR / TONE PRGRM defaults), and the raw
// reserved-byte regions are pre-seeded with the specific values that example
// shows (notably PrReser1=255, PrUndef3=7E 00 44 01, KyUndef2=44 01 44 01).
//
// numKeygroups must be in [1, MaxKeygroups]; out-of-range values are clamped.
// Each keygroup spans the full keyboard (24..127) with no sample assigned —
// the caller is expected to edit lower_key/upper_key/soft_sample per zone.
func NewDefaultProgram(name string, numKeygroups int) *Program {
	if numKeygroups < 1 {
		numKeygroups = 1
	}
	if numKeygroups > MaxKeygroups {
		numKeygroups = MaxKeygroups
	}

	p := &Program{
		Name:              name,
		KeyTilt:           0,
		PositionalXFade:   false,
		NumKeygroups:      uint8(numKeygroups),
		MidiProgramNumber: 0,
		EnableMidiProgram: true,
	}

	// Seed RawHeader with reserved-byte defaults from dxzl's example.
	// PrUndef3 at offset 36: 7E 00 44 01 (4 wire bytes).
	p.RawHeader[pHdrUndef3+0] = 0x7E
	p.RawHeader[pHdrUndef3+1] = 0x00
	p.RawHeader[pHdrUndef3+2] = 0x44
	p.RawHeader[pHdrUndef3+3] = 0x01
	// PrReser1 at offset 44: DB(255) = 7F 01.
	p.RawHeader[pHdrReser1+0] = 0x7F
	p.RawHeader[pHdrReser1+1] = 0x01

	p.Keygroups = make([]Keygroup, numKeygroups)
	for i := range p.Keygroups {
		p.Keygroups[i] = newDefaultKeygroup()
	}
	return p
}

func newDefaultKeygroup() Keygroup {
	kg := Keygroup{
		LowerKey: 24, UpperKey: 127,
		VelocitySwitch: 128,

		// Amp ADSR — basic "hold and release" envelope.
		AttackTime:   0,
		DecayTime:    80,
		SustainLevel: 99,
		ReleaseTime:  30,

		// Filter ADSR — modest envelope on the VCF.
		FilterAttackTime:   20,
		FilterDecayTime:    20,
		FilterSustainLevel: 20,
		FilterReleaseTime:  20,

		// Mod routing — light filter velocity + key tracking; mod-wheel-to-LFO.
		FilterVelInt:        10,
		FilterKeyTracking:   50,
		AttackVelInt:        0,
		VelReleaseInt:       0,
		LoudnessVelInt:      30,
		PitchWarpVelInt:     0,
		PitchWarpOffset:     0,
		PitchWarpRecovery:   99,
		AdsrEnvToVcfFilter:  0,
		AftertouchDepthMod:  0,
		ModWheelLfoDepthMod: 50,

		LfoBuildTime: 64,
		LfoRate:      42,
		LfoDepth:     0,

		ControlBits:    4, // vibrato desync on, transpose off (drum-style)
		VoiceOutAssign: 255, // ALL outputs
		MidiOffset:     0,
		VelXfade50pct:  64,

		SoftSampleName:   "",
		SoftTransposeRaw: 0,
		SoftFilter:       99,
		SoftLoudness:     0,

		LoudSampleName:   "",
		LoudTransposeRaw: 0,
		LoudFilter:       99,
		LoudLoudness:     0,
	}

	// KyUndef2 at offset 80: dxzl's example shows 44 01 44 01.
	// Unknown semantics ("Could this be BCD form for fine-pitch?") — preserve.
	kg.RawBytes[80] = 0x44
	kg.RawBytes[81] = 0x01
	kg.RawBytes[82] = 0x44
	kg.RawBytes[83] = 0x01

	return kg
}

func parseKeygroup(buf []byte) (*Keygroup, error) {
	if len(buf) != KeygroupSize {
		return nil, errors.New("keygroup buffer wrong size")
	}
	kg := &Keygroup{}
	copy(kg.RawBytes[:], buf)

	kg.UpperKey = sysex.DecodeDB(buf[kgUMK], buf[kgUMK+1])
	kg.LowerKey = sysex.DecodeDB(buf[kgLMK], buf[kgLMK+1])
	kg.VelocitySwitch = sysex.DecodeDB(buf[kgVST], buf[kgVST+1])

	kg.AttackTime = sysex.DecodeDB(buf[kgATK], buf[kgATK+1])
	kg.DecayTime = sysex.DecodeDB(buf[kgDCY], buf[kgDCY+1])
	kg.SustainLevel = sysex.DecodeDB(buf[kgSSTN], buf[kgSSTN+1])
	kg.ReleaseTime = sysex.DecodeDB(buf[kgRLSE], buf[kgRLSE+1])

	kg.FilterAttackTime = sysex.DecodeDB(buf[kgVCFAK], buf[kgVCFAK+1])
	kg.FilterDecayTime = sysex.DecodeDB(buf[kgVCFDY], buf[kgVCFDY+1])
	kg.FilterSustainLevel = sysex.DecodeDB(buf[kgVCFST], buf[kgVCFST+1])
	kg.FilterReleaseTime = sysex.DecodeDB(buf[kgVCFRL], buf[kgVCFRL+1])

	kg.FilterVelInt = sysex.DecodeDB(buf[kgFVI], buf[kgFVI+1])
	kg.FilterKeyTracking = sysex.DecodeDB(buf[kgFKI], buf[kgFKI+1])
	kg.AttackVelInt = sysex.DecodeDB(buf[kgAVI], buf[kgAVI+1])
	kg.VelReleaseInt = int8(sysex.DecodeDB(buf[kgRVI], buf[kgRVI+1]))
	kg.LoudnessVelInt = sysex.DecodeDB(buf[kgLVI], buf[kgLVI+1])
	kg.PitchWarpVelInt = sysex.DecodeDB(buf[kgPVI], buf[kgPVI+1])
	kg.PitchWarpOffset = int8(sysex.DecodeDB(buf[kgPAO], buf[kgPAO+1]))
	kg.PitchWarpRecovery = sysex.DecodeDB(buf[kgPST], buf[kgPST+1])
	kg.AdsrEnvToVcfFilter = int8(sysex.DecodeDB(buf[kgVCFAMNT], buf[kgVCFAMNT+1]))
	kg.AftertouchDepthMod = sysex.DecodeDB(buf[kgAFDI], buf[kgAFDI+1])
	kg.ModWheelLfoDepthMod = sysex.DecodeDB(buf[kgMWDI], buf[kgMWDI+1])

	kg.LfoBuildTime = sysex.DecodeDB(buf[kgVBDLY], buf[kgVBDLY+1])
	kg.LfoRate = sysex.DecodeDB(buf[kgVBRATE], buf[kgVBRATE+1])
	kg.LfoDepth = sysex.DecodeDB(buf[kgVBDPTH], buf[kgVBDPTH+1])

	kg.ControlBits = sysex.DecodeDB(buf[kgKBITS], buf[kgKBITS+1])
	kg.VoiceOutAssign = sysex.DecodeDB(buf[kgOPVOICE], buf[kgOPVOICE+1])
	kg.MidiOffset = sysex.DecodeDB(buf[kgKMDCHN], buf[kgKMDCHN+1])
	kg.VelXfade50pct = sysex.DecodeDB(buf[kgVTMX], buf[kgVTMX+1])

	kg.SoftSampleName = sysex.DecodeName(*(*[20]byte)(buf[kgNAMEFS : kgNAMEFS+20]))
	kg.SoftTransposeRaw = int16(sysex.DecodeDW(*(*[4]byte)(buf[kgTROFFS : kgTROFFS+4])))
	kg.SoftFilter = sysex.DecodeDB(buf[kgFLTFS], buf[kgFLTFS+1])
	kg.SoftLoudness = int8(sysex.DecodeDB(buf[kgLOUDFS], buf[kgLOUDFS+1]))

	kg.LoudSampleName = sysex.DecodeName(*(*[20]byte)(buf[kgNAMESS : kgNAMESS+20]))
	kg.LoudTransposeRaw = int16(sysex.DecodeDW(*(*[4]byte)(buf[kgTROFSS : kgTROFSS+4])))
	kg.LoudFilter = sysex.DecodeDB(buf[kgFLTSS], buf[kgFLTSS+1])
	kg.LoudLoudness = int8(sysex.DecodeDB(buf[kgLOUDSS], buf[kgLOUDSS+1]))

	return kg, nil
}

// EncodePayload re-encodes a Program back into a PRGM payload, preserving any
// reserved/undefined bytes via RawHeader and RawBytes.
func (p *Program) EncodePayload() []byte {
	out := make([]byte, 0, ProgramHeaderSize+len(p.Keygroups)*KeygroupSize)

	// Header: start from raw, overwrite known fields.
	hdr := make([]byte, ProgramHeaderSize)
	copy(hdr, p.RawHeader[:])
	name := sysex.EncodeName(p.Name)
	copy(hdr[pHdrName:pHdrName+20], name[:])
	kt := sysex.EncodeDW(uint16(p.KeyTilt))
	copy(hdr[pHdrKeyTilt:pHdrKeyTilt+4], kt[:])
	{
		var v byte
		if p.PositionalXFade {
			v = 1
		}
		lo, hi := sysex.EncodeDB(v)
		hdr[pHdrPosXFade], hdr[pHdrPosXFade+1] = lo, hi
	}
	{
		lo, hi := sysex.EncodeDB(p.NumKeygroups)
		hdr[pHdrNumKgs], hdr[pHdrNumKgs+1] = lo, hi
	}
	{
		lo, hi := sysex.EncodeDB(p.MidiProgramNumber)
		hdr[pHdrMidiPgm], hdr[pHdrMidiPgm+1] = lo, hi
	}
	{
		var v byte
		if p.EnableMidiProgram {
			v = 255
		}
		lo, hi := sysex.EncodeDB(v)
		hdr[pHdrEnableMP], hdr[pHdrEnableMP+1] = lo, hi
	}
	out = append(out, hdr...)

	for i := range p.Keygroups {
		out = append(out, p.Keygroups[i].encode()...)
	}
	return out
}

func (kg *Keygroup) encode() []byte {
	out := make([]byte, KeygroupSize)
	copy(out, kg.RawBytes[:])

	put1 := func(off int, v byte) {
		lo, hi := sysex.EncodeDB(v)
		out[off], out[off+1] = lo, hi
	}

	put1(kgUMK, kg.UpperKey)
	put1(kgLMK, kg.LowerKey)
	put1(kgVST, kg.VelocitySwitch)
	put1(kgATK, kg.AttackTime)
	put1(kgDCY, kg.DecayTime)
	put1(kgSSTN, kg.SustainLevel)
	put1(kgRLSE, kg.ReleaseTime)
	put1(kgVCFAK, kg.FilterAttackTime)
	put1(kgVCFDY, kg.FilterDecayTime)
	put1(kgVCFST, kg.FilterSustainLevel)
	put1(kgVCFRL, kg.FilterReleaseTime)
	put1(kgFVI, kg.FilterVelInt)
	put1(kgFKI, kg.FilterKeyTracking)
	put1(kgAVI, kg.AttackVelInt)
	put1(kgRVI, uint8(kg.VelReleaseInt))
	put1(kgLVI, kg.LoudnessVelInt)
	put1(kgPVI, kg.PitchWarpVelInt)
	put1(kgPAO, uint8(kg.PitchWarpOffset))
	put1(kgPST, kg.PitchWarpRecovery)
	put1(kgVCFAMNT, uint8(kg.AdsrEnvToVcfFilter))
	put1(kgAFDI, kg.AftertouchDepthMod)
	put1(kgMWDI, kg.ModWheelLfoDepthMod)
	put1(kgVBDLY, kg.LfoBuildTime)
	put1(kgVBRATE, kg.LfoRate)
	put1(kgVBDPTH, kg.LfoDepth)
	put1(kgKBITS, kg.ControlBits)
	put1(kgOPVOICE, kg.VoiceOutAssign)
	put1(kgKMDCHN, kg.MidiOffset)
	put1(kgVTMX, kg.VelXfade50pct)

	soft := sysex.EncodeName(kg.SoftSampleName)
	copy(out[kgNAMEFS:kgNAMEFS+20], soft[:])
	sf := sysex.EncodeDW(uint16(kg.SoftTransposeRaw))
	copy(out[kgTROFFS:kgTROFFS+4], sf[:])
	put1(kgFLTFS, kg.SoftFilter)
	put1(kgLOUDFS, uint8(kg.SoftLoudness))

	loud := sysex.EncodeName(kg.LoudSampleName)
	copy(out[kgNAMESS:kgNAMESS+20], loud[:])
	lf := sysex.EncodeDW(uint16(kg.LoudTransposeRaw))
	copy(out[kgTROFSS:kgTROFSS+4], lf[:])
	put1(kgFLTSS, kg.LoudFilter)
	put1(kgLOUDSS, uint8(kg.LoudLoudness))

	return out
}
