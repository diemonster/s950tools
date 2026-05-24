package protocol

import (
	"encoding/hex"
	"fmt"
	"math"
)

// SampleSpec describes one sample that should be uploaded as part of a kit.
// The file path is resolved relative to the JSON file the SampleSpec was read
// from. When the program JSON's `samples` array is non-empty, put-program
// first uploads each sample (auto-picking empty slots, skipping any whose
// `name` is already present in the device catalog), then sets the SPRM Name
// field so keygroups can reference the sample by its declared name.
//
// Only `file` is required; everything else has sensible defaults.
type SampleSpec struct {
	// File is the path to the WAV or AIFF, relative to the JSON file's
	// directory.
	File string `json:"file"`
	// Name is the S950 sample name (max 10 ASCII chars) that the device
	// SPRM will be set to after upload. Keygroups reference samples by this
	// name. If empty, defaults to the file's stem uppercased.
	Name string `json:"name,omitempty"`
	// Rate accepts the same values as put-sample's --rate flag: empty (use
	// source rate), a number in Hz ("10000"), or an alias ("sp1200", "lofi",
	// "mpc60", "telephone", or "s950-7..40").
	Rate string `json:"rate,omitempty"`
	// ChannelMode: "left" | "right" | "mix" (default "mix").
	ChannelMode string `json:"channel_mode,omitempty"`
	// Tune in semitones (float; 1/16 precision on the wire). Adjusts the
	// sample's SNOMP (home pitch) — same semantics as put-sample --tune.
	Tune float64 `json:"tune,omitempty"`
	// MaxFrames optionally truncates the source after resampling.
	MaxFrames int `json:"max_frames,omitempty"`
	// Loop / mode options mirror put-sample's flags.
	LoopStart uint32 `json:"loop_start,omitempty"`
	LoopEnd   uint32 `json:"loop_end,omitempty"`
	Mode      uint8  `json:"mode,omitempty"`
}

// JSON-friendly mirror of Program. Tune fields are exposed as float semitones
// (e.g. -3.5) instead of the wire-format 1/16-semitone integer; same goes for
// raw byte arrays which are rendered as compact hex strings rather than
// 76-element JSON number arrays.
//
// Samples is an optional manifest section the CLI's put-program command
// honours: when present, samples are uploaded (with the listed name, rate,
// tune, etc.) before the program is sent. The encode/decode for the program
// itself ignores Samples.
type ProgramJSON struct {
	Name              string         `json:"name"`
	KeyTilt           int16          `json:"key_tilt"`
	PositionalXFade   bool           `json:"positional_xfade"`
	NumKeygroups      uint8          `json:"num_keygroups"`
	MidiProgramNumber uint8          `json:"midi_program_number"`
	EnableMidiProgram bool           `json:"enable_midi_program"`
	Samples           []SampleSpec   `json:"samples,omitempty"`
	Keygroups         []KeygroupJSON `json:"keygroups"`
	RawHeaderHex      string         `json:"_raw_header_hex"` // 76 bytes = 152 hex chars
}

type KeygroupJSON struct {
	// Key range and velocity routing.
	LowerKey       uint8 `json:"lower_key"`
	UpperKey       uint8 `json:"upper_key"`
	VelocitySwitch uint8 `json:"velocity_switch"`

	// Amp envelope.
	AttackTime   uint8 `json:"attack"`
	DecayTime    uint8 `json:"decay"`
	SustainLevel uint8 `json:"sustain"`
	ReleaseTime  uint8 `json:"release"`

	// Filter envelope.
	FilterAttackTime   uint8 `json:"filter_attack"`
	FilterDecayTime    uint8 `json:"filter_decay"`
	FilterSustainLevel uint8 `json:"filter_sustain"`
	FilterReleaseTime  uint8 `json:"filter_release"`

	// Mod routing.
	FilterVelInt        uint8 `json:"filter_vel_int"`
	FilterKeyTracking   uint8 `json:"filter_key_tracking"`
	AttackVelInt        uint8 `json:"attack_vel_int"`
	VelReleaseInt       int8  `json:"vel_release_int"`
	LoudnessVelInt      uint8 `json:"loudness_vel_int"`
	PitchWarpVelInt     uint8 `json:"pitch_warp_vel_int"`
	PitchWarpOffset     int8  `json:"pitch_warp_offset"`
	PitchWarpRecovery   uint8 `json:"pitch_warp_recovery"`
	AdsrEnvToVcfFilter  int8  `json:"adsr_to_vcf"`
	AftertouchDepthMod  uint8 `json:"aftertouch_depth_mod"`
	ModWheelLfoDepthMod uint8 `json:"mod_wheel_lfo_depth_mod"`

	// LFO.
	LfoBuildTime uint8 `json:"lfo_build_time"`
	LfoRate      uint8 `json:"lfo_rate"`
	LfoDepth     uint8 `json:"lfo_depth"`

	// Misc.
	ControlBits    uint8 `json:"control_bits"`
	VoiceOutAssign uint8 `json:"voice_out_assign"`
	MidiOffset     uint8 `json:"midi_offset"`
	VelXfade50pct  uint8 `json:"vel_xfade_50pct"`

	// Soft sample (primary).
	SoftSampleName string  `json:"soft_sample"`
	SoftTune       float64 `json:"soft_tune"` // float semitones (1/16 resolution)
	SoftFilter     uint8   `json:"soft_filter"`
	SoftLoudness   int8    `json:"soft_loudness"`

	// Loud sample (velocity-switched).
	LoudSampleName string  `json:"loud_sample"`
	LoudTune       float64 `json:"loud_tune"`
	LoudFilter     uint8   `json:"loud_filter"`
	LoudLoudness   int8    `json:"loud_loudness"`

	RawBytesHex string `json:"_raw_bytes_hex"` // 140 bytes = 280 hex chars
}

// ToJSON returns a JSON-friendly view of the Program.
func (p *Program) ToJSON() ProgramJSON {
	out := ProgramJSON{
		Name:              p.Name,
		KeyTilt:           p.KeyTilt,
		PositionalXFade:   p.PositionalXFade,
		NumKeygroups:      p.NumKeygroups,
		MidiProgramNumber: p.MidiProgramNumber,
		EnableMidiProgram: p.EnableMidiProgram,
		Keygroups:         make([]KeygroupJSON, len(p.Keygroups)),
		RawHeaderHex:      hex.EncodeToString(p.RawHeader[:]),
	}
	for i := range p.Keygroups {
		out.Keygroups[i] = p.Keygroups[i].toJSON()
	}
	return out
}

func (kg *Keygroup) toJSON() KeygroupJSON {
	return KeygroupJSON{
		LowerKey:            kg.LowerKey,
		UpperKey:            kg.UpperKey,
		VelocitySwitch:      kg.VelocitySwitch,
		AttackTime:          kg.AttackTime,
		DecayTime:           kg.DecayTime,
		SustainLevel:        kg.SustainLevel,
		ReleaseTime:         kg.ReleaseTime,
		FilterAttackTime:    kg.FilterAttackTime,
		FilterDecayTime:     kg.FilterDecayTime,
		FilterSustainLevel:  kg.FilterSustainLevel,
		FilterReleaseTime:   kg.FilterReleaseTime,
		FilterVelInt:        kg.FilterVelInt,
		FilterKeyTracking:   kg.FilterKeyTracking,
		AttackVelInt:        kg.AttackVelInt,
		VelReleaseInt:       kg.VelReleaseInt,
		LoudnessVelInt:      kg.LoudnessVelInt,
		PitchWarpVelInt:     kg.PitchWarpVelInt,
		PitchWarpOffset:     kg.PitchWarpOffset,
		PitchWarpRecovery:   kg.PitchWarpRecovery,
		AdsrEnvToVcfFilter:  kg.AdsrEnvToVcfFilter,
		AftertouchDepthMod:  kg.AftertouchDepthMod,
		ModWheelLfoDepthMod: kg.ModWheelLfoDepthMod,
		LfoBuildTime:        kg.LfoBuildTime,
		LfoRate:             kg.LfoRate,
		LfoDepth:            kg.LfoDepth,
		ControlBits:         kg.ControlBits,
		VoiceOutAssign:      kg.VoiceOutAssign,
		MidiOffset:          kg.MidiOffset,
		VelXfade50pct:       kg.VelXfade50pct,
		SoftSampleName:      kg.SoftSampleName,
		SoftTune:            float64(kg.SoftTransposeRaw) / 16.0,
		SoftFilter:          kg.SoftFilter,
		SoftLoudness:        kg.SoftLoudness,
		LoudSampleName:      kg.LoudSampleName,
		LoudTune:            float64(kg.LoudTransposeRaw) / 16.0,
		LoudFilter:          kg.LoudFilter,
		LoudLoudness:        kg.LoudLoudness,
		RawBytesHex:         hex.EncodeToString(kg.RawBytes[:]),
	}
}

// FromJSON populates a Program from a ProgramJSON. The _raw_header_hex and
// _raw_bytes_hex fields are restored first so undocumented bytes round-trip,
// then named fields overwrite. Length checks ensure the user didn't truncate
// or extend the hex payloads.
func (p *Program) FromJSON(j *ProgramJSON) error {
	rawHdr, err := hex.DecodeString(j.RawHeaderHex)
	if err != nil {
		return fmt.Errorf("decode _raw_header_hex: %w", err)
	}
	if len(rawHdr) != ProgramHeaderSize {
		return fmt.Errorf("_raw_header_hex must be %d bytes, got %d",
			ProgramHeaderSize, len(rawHdr))
	}
	copy(p.RawHeader[:], rawHdr)

	p.Name = j.Name
	p.KeyTilt = j.KeyTilt
	p.PositionalXFade = j.PositionalXFade
	p.NumKeygroups = j.NumKeygroups
	p.MidiProgramNumber = j.MidiProgramNumber
	p.EnableMidiProgram = j.EnableMidiProgram

	p.Keygroups = make([]Keygroup, len(j.Keygroups))
	for i := range j.Keygroups {
		if err := p.Keygroups[i].fromJSON(&j.Keygroups[i]); err != nil {
			return fmt.Errorf("keygroup %d: %w", i, err)
		}
	}
	// If the user changed the keygroup count by editing the array, sync it.
	if len(p.Keygroups) > 0 {
		p.NumKeygroups = uint8(len(p.Keygroups))
	}
	return nil
}

func (kg *Keygroup) fromJSON(j *KeygroupJSON) error {
	raw, err := hex.DecodeString(j.RawBytesHex)
	if err != nil {
		return fmt.Errorf("decode _raw_bytes_hex: %w", err)
	}
	if len(raw) != KeygroupSize {
		return fmt.Errorf("_raw_bytes_hex must be %d bytes, got %d",
			KeygroupSize, len(raw))
	}
	copy(kg.RawBytes[:], raw)

	kg.LowerKey = j.LowerKey
	kg.UpperKey = j.UpperKey
	kg.VelocitySwitch = j.VelocitySwitch
	kg.AttackTime = j.AttackTime
	kg.DecayTime = j.DecayTime
	kg.SustainLevel = j.SustainLevel
	kg.ReleaseTime = j.ReleaseTime
	kg.FilterAttackTime = j.FilterAttackTime
	kg.FilterDecayTime = j.FilterDecayTime
	kg.FilterSustainLevel = j.FilterSustainLevel
	kg.FilterReleaseTime = j.FilterReleaseTime
	kg.FilterVelInt = j.FilterVelInt
	kg.FilterKeyTracking = j.FilterKeyTracking
	kg.AttackVelInt = j.AttackVelInt
	kg.VelReleaseInt = j.VelReleaseInt
	kg.LoudnessVelInt = j.LoudnessVelInt
	kg.PitchWarpVelInt = j.PitchWarpVelInt
	kg.PitchWarpOffset = j.PitchWarpOffset
	kg.PitchWarpRecovery = j.PitchWarpRecovery
	kg.AdsrEnvToVcfFilter = j.AdsrEnvToVcfFilter
	kg.AftertouchDepthMod = j.AftertouchDepthMod
	kg.ModWheelLfoDepthMod = j.ModWheelLfoDepthMod
	kg.LfoBuildTime = j.LfoBuildTime
	kg.LfoRate = j.LfoRate
	kg.LfoDepth = j.LfoDepth
	kg.ControlBits = j.ControlBits
	kg.VoiceOutAssign = j.VoiceOutAssign
	kg.MidiOffset = j.MidiOffset
	kg.VelXfade50pct = j.VelXfade50pct
	kg.SoftSampleName = j.SoftSampleName
	kg.SoftTransposeRaw = semitonesToRaw(j.SoftTune)
	kg.SoftFilter = j.SoftFilter
	kg.SoftLoudness = j.SoftLoudness
	kg.LoudSampleName = j.LoudSampleName
	kg.LoudTransposeRaw = semitonesToRaw(j.LoudTune)
	kg.LoudFilter = j.LoudFilter
	kg.LoudLoudness = j.LoudLoudness
	return nil
}

// semitonesToRaw maps float semitones to the S950's 1/16-semitone integer.
// Clamps to int16 range to avoid silent wrap.
func semitonesToRaw(semitones float64) int16 {
	v := math.Round(semitones * 16.0)
	if v > math.MaxInt16 {
		v = math.MaxInt16
	}
	if v < math.MinInt16 {
		v = math.MinInt16
	}
	return int16(v)
}
