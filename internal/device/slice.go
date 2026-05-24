package device

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/sample"
)

// SliceSpec is one slice extraction request. Offsets are S950 words
// (12-bit) into the source sample's word buffer.
type SliceSpec struct {
	// Name is the SPRM name for the resulting sample (max 10 ASCII chars).
	Name string
	// StartWord is the source-relative offset where the slice begins.
	StartWord uint32
	// LengthWords is the slice's length in words. Must be >= 200
	// (S950 minimum). Lengths < 200 are rejected; the caller should
	// pad or refuse to slice that small.
	LengthWords uint32
	// LoopMode picks the per-slice replay flag:
	//   "one-shot" — sample plays through and stops
	//   "loop"     — wire mode 0, loops the [LoopStart..LoopStart+LoopLength]
	//   "ping-pong"— wire mode 1, alternating playback over the same region
	// Empty string is treated as "one-shot".
	LoopMode string
	// LoopStartInSlice is the loop point's start, relative to the
	// slice's first word (0 = start of slice). Only used when LoopMode
	// is "loop" or "ping-pong".
	LoopStartInSlice uint32
	// LoopLengthInSlice is the loop's length in words. Loop end =
	// LoopStartInSlice + LoopLengthInSlice. If the resulting length is
	// < 5, the slice is treated as one-shot regardless of LoopMode.
	LoopLengthInSlice uint32
}

// SlicePayload is one slice ready for upload — words extracted from
// the source plus the PutSampleOpts that go with them. Slot number
// (PutSampleOpts.Num) is left as 0 here; the orchestration layer fills
// it in based on free-slot allocation right before sending.
type SlicePayload struct {
	// Name is the SPRM name (matches SliceSpec.Name after sanitisation
	// — see SanitizeName for the truncation rules).
	Name string
	// Words is the slice's audio data (independent copy of the source
	// region, safe to send).
	Words []uint16
	// Opts has the rate + loop config + mode resolved from the slice
	// spec. The caller fills in Opts.Num with the chosen slot.
	Opts PutSampleOpts
}

// Errors from BuildSlices.
var (
	ErrSliceOutOfRange = errors.New("slice extends past source")
	ErrSliceTooShort   = fmt.Errorf("slice shorter than S950 minimum (%d words)", sample.MinTotalWords)
	ErrTooManySlices   = errors.New("too many slices (max 31 keygroups per program)")
	ErrNoSlices        = errors.New("no slices specified")
)

// BuildSlices extracts every slice from `source` and returns one
// SlicePayload per slice. Pure: no MIDI, no I/O — safe to unit-test.
//
// The function validates the whole batch up front and returns the
// first failure so the host can abort before any upload begins
// (better than failing mid-transfer after some slices already landed
// on the device).
func BuildSlices(source []uint16, sourceRateHz uint32, slices []SliceSpec) ([]SlicePayload, error) {
	if len(slices) == 0 {
		return nil, ErrNoSlices
	}
	srcLen := uint32(len(source))

	out := make([]SlicePayload, 0, len(slices))
	for i, sl := range slices {
		end := sl.StartWord + sl.LengthWords
		if end > srcLen {
			return nil, fmt.Errorf("slice %d: %w (start=%d length=%d source=%d)",
				i, ErrSliceOutOfRange, sl.StartWord, sl.LengthWords, srcLen)
		}
		if sl.LengthWords < sample.MinTotalWords {
			return nil, fmt.Errorf("slice %d: %w", i, ErrSliceTooShort)
		}
		// Make an independent copy so caller mutation of the source
		// later can't corrupt an in-flight upload.
		words := make([]uint16, sl.LengthWords)
		copy(words, source[sl.StartWord:end])

		opts := resolveLoop(sl)
		opts.SampleRateHz = sourceRateHz

		out = append(out, SlicePayload{
			Name:  SanitizeName(sl.Name),
			Words: words,
			Opts:  opts,
		})
	}
	return out, nil
}

// resolveLoop converts the slice's loop spec into the wire-level
// PutSampleOpts fields (LoopStart / LoopEnd / Mode). Honors the
// S950's "loop_start >= total - 5" sentinel for one-shot playback.
func resolveLoop(sl SliceSpec) PutSampleOpts {
	total := sl.LengthWords
	loopStart := sl.LoopStartInSlice
	loopEnd := sl.LoopStartInSlice + sl.LoopLengthInSlice

	var mode byte
	switch sl.LoopMode {
	case "ping-pong":
		mode = 1
	default:
		mode = 0
	}

	// Force one-shot when explicitly requested OR when the loop region
	// is too small to be useful — matches the sample-dump protocol's
	// "loop_start >= total - 5 == non-looping" rule.
	if sl.LoopMode == "" || sl.LoopMode == "one-shot" || loopEnd <= loopStart+5 {
		if total < 5 {
			loopStart = 0
		} else {
			loopStart = total - 5
		}
		loopEnd = total
		mode = 0
	}
	return PutSampleOpts{
		LoopStart: loopStart,
		LoopEnd:   loopEnd,
		Mode:      mode,
	}
}

// SanitizeName trims and uppercases a name to fit the S950's 10-char
// SPRM limit. Non-ASCII bytes are dropped; spaces are preserved.
func SanitizeName(s string) string {
	var b strings.Builder
	b.Grow(10)
	for _, r := range s {
		if b.Len() == 10 {
			break
		}
		if r >= 0x20 && r < 0x7F {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SliceNames returns base_01..base_NN names sized to fit the 10-char
// limit. Truncates `base` if needed so the trailing "_NN" always fits.
// Examples: ("BEATS", 8)   → ["BEATS_01"..."BEATS_08"]
//
//	("AMENBREAKS", 8) → ["AMENBR_01"..."AMENBR_08"]
func SliceNames(base string, n int) []string {
	base = SanitizeName(base)
	suffixLen := 3 // "_NN" — two digits is enough since n ≤ 31
	if n >= 100 {
		suffixLen = 4
	}
	maxBase := 10 - suffixLen
	if len(base) > maxBase {
		base = base[:maxBase]
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = fmt.Sprintf("%s_%02d", base, i+1)
	}
	return out
}

// BuildSliceProgram constructs a Program that maps each slice
// chromatically to one keygroup, starting at baseMidiKey. The
// sample-name strings on each keygroup match the names BuildSlices
// produced (and ultimately the SPRM names sent to the device).
//
// Each keygroup is a single-key zone (LowerKey == UpperKey) with
// VelocitySwitch=128 (soft layer only). The caller can post-process
// the program for ranged kits or velocity layering before sending.
func BuildSliceProgram(programName string, slices []SliceSpec, baseMidiKey uint8) (*protocol.Program, error) {
	if len(slices) == 0 {
		return nil, ErrNoSlices
	}
	if len(slices) > protocol.MaxKeygroups {
		return nil, fmt.Errorf("%w (got %d, max %d)", ErrTooManySlices, len(slices), protocol.MaxKeygroups)
	}
	if int(baseMidiKey)+len(slices)-1 > 127 {
		return nil, fmt.Errorf("slice mapping runs past MIDI 127 (base=%d, n=%d)", baseMidiKey, len(slices))
	}

	p := protocol.NewDefaultProgram(programName, len(slices))
	for i := range p.Keygroups {
		key := baseMidiKey + uint8(i)
		kg := &p.Keygroups[i]
		kg.LowerKey = key
		kg.UpperKey = key
		kg.VelocitySwitch = 128
		kg.SoftSampleName = SanitizeName(slices[i].Name)
		kg.LoudSampleName = ""
	}
	return p, nil
}
