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
	// directory (or absolute).
	File string `json:"file"`

	// Name is the S950 sample name (max 10 ASCII chars) that the device
	// SPRM will be set to after upload. Keygroups reference samples by
	// this name. If empty, defaults to the file's stem uppercased.
	Name string `json:"name,omitempty"`

	// Rate accepts the same values as put-sample's --rate flag: empty
	// (use source rate), a number in Hz ("10000"), or an alias such as
	// "sp1200", "lofi", "mpc60", "telephone", or "s950-7..40".
	Rate string `json:"rate,omitempty"`

	// ChannelMode controls stereo-to-mono folding: "left", "right", or
	// "mix" (default).
	ChannelMode string `json:"channel_mode,omitempty"`

	// Tune in semitones (float; 1/16 precision on the wire). Adjusts the
	// sample's SNOMP — same semantics as put-sample --tune. For
	// per-keygroup tuning, use the keygroup's soft_tune instead.
	Tune float64 `json:"tune,omitempty"`

	// MaxFrames optionally truncates the source after resampling.
	MaxFrames int `json:"max_frames,omitempty"`

	// LoopStart is the loop start point (words), used together with LoopEnd.
	LoopStart uint32 `json:"loop_start,omitempty"`

	// LoopEnd is the loop end point (words). When LoopEnd-LoopStart < 5,
	// the sample is treated as one-shot.
	LoopEnd uint32 `json:"loop_end,omitempty"`

	// Mode is the replay mode: 0 = looping, 1 = alternating.
	Mode uint8 `json:"mode,omitempty"`
}

// ProgramJSON is the JSON-friendly mirror of Program. Tune fields are
// exposed as float semitones (e.g. -3.5) instead of the wire-format
// 1/16-semitone integer; raw byte arrays are rendered as compact hex
// strings rather than 76- or 140-element JSON number arrays.
//
// Samples is an optional manifest section the CLI's put-program command
// honours: when present, samples are uploaded (with the listed name, rate,
// tune, etc.) before the program is sent. The encode/decode for the program
// itself ignores Samples — it's a CLI-level helper, not a wire field.
type ProgramJSON struct {
	// Name is the 10-char program name displayed on the S950 front panel.
	Name string `json:"name"`

	// KeyTilt is the loudness-vs-key scaling, signed -50..+50.
	KeyTilt int16 `json:"key_tilt"`

	// PositionalXFade enables positional crossfade across the keygroup
	// range when true.
	PositionalXFade bool `json:"positional_xfade"`

	// NumKeygroups is the count of keygroups in this program (1..31).
	// Set from len(Keygroups) on encode.
	NumKeygroups uint8 `json:"num_keygroups"`

	// MidiProgramNumber is the MIDI Program Change number this program
	// responds to (0..127), if EnableMidiProgram is true.
	MidiProgramNumber uint8 `json:"midi_program_number"`

	// EnableMidiProgram enables MIDI Program Change reception (S950 only).
	EnableMidiProgram bool `json:"enable_midi_program"`

	// Samples is the (optional) sample manifest. When set, put-program
	// uploads each entry before sending the program.
	Samples []SampleSpec `json:"samples,omitempty"`

	// Keygroups define the keyboard-range-to-sample mapping plus
	// per-keygroup envelope/filter/tune settings.
	Keygroups []KeygroupJSON `json:"keygroups"`

	// RawHeaderHex preserves the full 76-byte wire payload of the program
	// header (152 hex chars). Allows undocumented bytes to round-trip
	// without us needing to model every field.
	RawHeaderHex string `json:"_raw_header_hex"`
}

// KeygroupJSON is the JSON-friendly mirror of Keygroup. All fields the
// dxzl/akai-s950 reference names are exposed here; unknown bytes are
// preserved in RawBytesHex for safe round-trip.
type KeygroupJSON struct {
	// LowerKey is the lowest MIDI note that triggers this keygroup (24..127).
	LowerKey uint8 `json:"lower_key"`

	// UpperKey is the highest MIDI note in the keygroup's range.
	UpperKey uint8 `json:"upper_key"`

	// VelocitySwitch is the velocity threshold above which the "loud"
	// sample plays instead of the "soft" one (0..128).
	VelocitySwitch uint8 `json:"velocity_switch"`

	// AttackTime sets the amp envelope's attack stage (0..99).
	AttackTime uint8 `json:"attack"`

	// DecayTime sets the amp envelope's decay stage (0..99).
	DecayTime uint8 `json:"decay"`

	// SustainLevel sets the amp envelope's sustain level (0..99).
	SustainLevel uint8 `json:"sustain"`

	// ReleaseTime sets the amp envelope's release stage (0..99).
	ReleaseTime uint8 `json:"release"`

	// FilterAttackTime sets the filter envelope's attack stage (0..99).
	FilterAttackTime uint8 `json:"filter_attack"`

	// FilterDecayTime sets the filter envelope's decay stage (0..99).
	FilterDecayTime uint8 `json:"filter_decay"`

	// FilterSustainLevel sets the filter envelope's sustain level (0..99).
	FilterSustainLevel uint8 `json:"filter_sustain"`

	// FilterReleaseTime sets the filter envelope's release stage (0..99).
	FilterReleaseTime uint8 `json:"filter_release"`

	// FilterVelInt sets velocity-to-filter modulation depth (0..99).
	FilterVelInt uint8 `json:"filter_vel_int"`

	// FilterKeyTracking sets filter keyboard tracking; 50 = 1V/oct
	// equivalent (0..99).
	FilterKeyTracking uint8 `json:"filter_key_tracking"`

	// AttackVelInt sets velocity-to-attack-time modulation depth (0..99).
	AttackVelInt uint8 `json:"attack_vel_int"`

	// VelReleaseInt is signed (-50..+50): how velocity affects release time.
	VelReleaseInt int8 `json:"vel_release_int"`

	// LoudnessVelInt sets velocity-to-loudness modulation depth (0..99).
	LoudnessVelInt uint8 `json:"loudness_vel_int"`

	// PitchWarpVelInt sets velocity-to-pitch-warp modulation depth (0..99).
	PitchWarpVelInt uint8 `json:"pitch_warp_vel_int"`

	// PitchWarpOffset is the pitch-warp initial offset (-50..+50).
	PitchWarpOffset int8 `json:"pitch_warp_offset"`

	// PitchWarpRecovery is the pitch-warp recovery time (0..99).
	PitchWarpRecovery uint8 `json:"pitch_warp_recovery"`

	// AdsrEnvToVcfFilter is how much the amp ADSR modulates the VCF
	// filter (-50..+50).
	AdsrEnvToVcfFilter int8 `json:"adsr_to_vcf"`

	// AftertouchDepthMod sets channel-pressure-to-pitch-warp depth (0..99).
	AftertouchDepthMod uint8 `json:"aftertouch_depth_mod"`

	// ModWheelLfoDepthMod sets mod-wheel-to-LFO-depth modulation (0..99).
	ModWheelLfoDepthMod uint8 `json:"mod_wheel_lfo_depth_mod"`

	// LfoBuildTime is the LFO delay/ramp-in time (0..99).
	LfoBuildTime uint8 `json:"lfo_build_time"`

	// LfoRate is the LFO speed (0..99).
	LfoRate uint8 `json:"lfo_rate"`

	// LfoDepth is the LFO depth applied at the source (0..99).
	LfoDepth uint8 `json:"lfo_depth"`

	// ControlBits is a bit-field of behaviour flags (dxzl reference):
	//   bit 0 (1): transpose OFF (clear = transpose on, default)
	//   bit 1 (2): velocity-xfade on
	//   bit 2 (4): vibrato-desync on (set by default)
	//   bit 3 (8): one-shot trigger mode
	//   bit 4 (16): velocity-release mode (1 = note-on triggers release)
	//   bit 5 (32): velocity-xfade curve modification
	// Default value (4) = vibrato-desync on, transpose enabled.
	ControlBits uint8 `json:"control_bits"`

	// VoiceOutAssign routes the keygroup's audio: 0..7 = mono outs,
	// 8 = left group (0..3), 9 = right group (4..7), 255 = ALL outputs.
	VoiceOutAssign uint8 `json:"voice_out_assign"`

	// MidiOffset shifts MIDI input by N semitones for this keygroup (0..15).
	MidiOffset uint8 `json:"midi_offset"`

	// VelXfade50pct is the velocity at which the soft/loud crossfade is
	// 50/50 (0..127).
	VelXfade50pct uint8 `json:"vel_xfade_50pct"`

	// SoftSampleName is the 10-char name of the sample this keygroup plays
	// for velocities below VelocitySwitch (i.e. the primary sample).
	// The S950 looks up the sample by name across the sample bank.
	SoftSampleName string `json:"soft_sample"`

	// SoftTune is per-keygroup transpose for the soft sample, in float
	// semitones (1/16 precision on wire). Use this rather than touching
	// the sample's SNOMP to adjust pitch per-keygroup.
	SoftTune float64 `json:"soft_tune"`

	// SoftFilter sets the filter cutoff for the soft sample (0..99;
	// 99 = brightest).
	SoftFilter uint8 `json:"soft_filter"`

	// SoftLoudness sets the loudness offset for the soft sample (-50..+50,
	// units of .375 dB).
	SoftLoudness int8 `json:"soft_loudness"`

	// LoudSampleName is the 10-char name of the "loud" sample played for
	// velocities at or above VelocitySwitch. Empty means same as soft.
	LoudSampleName string `json:"loud_sample"`

	// LoudTune is per-keygroup transpose for the loud sample, in float
	// semitones.
	LoudTune float64 `json:"loud_tune"`

	// LoudFilter sets the filter cutoff for the loud sample (0..99).
	LoudFilter uint8 `json:"loud_filter"`

	// LoudLoudness sets the loudness offset for the loud sample (-50..+50).
	LoudLoudness int8 `json:"loud_loudness"`

	// RawBytesHex preserves the full 140-byte wire payload of this
	// keygroup (280 hex chars). Lets undocumented bytes round-trip safely.
	RawBytesHex string `json:"_raw_bytes_hex"`
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
	if len(p.Keygroups) > 0 {
		p.NumKeygroups = uint8(len(p.Keygroups))
	}
	return nil
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
