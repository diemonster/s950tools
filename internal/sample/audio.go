package sample

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/go-audio/aiff"
	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

// ChannelMode picks how multi-channel input is folded to mono for the S950
// (which only stores mono).
type ChannelMode int

const (
	// ChannelMix averages all channels (default — preserves overall energy
	// but cancels phase-inverted material).
	ChannelMix ChannelMode = iota
	// ChannelLeft uses only channel 0.
	ChannelLeft
	// ChannelRight uses only channel 1 (falls back to 0 on mono input).
	ChannelRight
)

// ParseChannelMode maps "mix" | "left" | "right" (case-insensitive) to a
// ChannelMode. Empty string returns ChannelMix.
func ParseChannelMode(s string) (ChannelMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "mix":
		return ChannelMix, nil
	case "left", "l":
		return ChannelLeft, nil
	case "right", "r":
		return ChannelRight, nil
	default:
		return 0, fmt.Errorf("unknown channel mode %q (want left|right|mix)", s)
	}
}

// LoadedAudio is a mono 16-bit PCM stream plus its source sample rate, ready
// for S950 conversion.
type LoadedAudio struct {
	PCM        []int16
	SampleRate uint32 // Hz
}

// LoadAudio reads a WAV or AIFF file and folds it to mono int16 PCM. Format
// is detected from the path's extension (.wav, .aif, .aiff). Channel mode
// controls how stereo input becomes mono.
func LoadAudio(path string, mode ChannelMode) (*LoadedAudio, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".wav":
		return loadWAV(path, mode)
	case ".aif", ".aiff":
		return loadAIFF(path, mode)
	default:
		return nil, fmt.Errorf("unsupported audio extension %q (want .wav, .aif, .aiff)", ext)
	}
}

// WavAudioFormat constants per the RIFF spec.
const (
	wavFormatPCM   uint16 = 0x0001 // signed integer PCM
	wavFormatFloat uint16 = 0x0003 // IEEE 32/64-bit floating point
)

func loadWAV(path string, mode ChannelMode) (*LoadedAudio, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := wav.NewDecoder(f)
	if !dec.IsValidFile() {
		return nil, errors.New("not a valid WAV file")
	}
	buf, err := dec.FullPCMBuffer()
	if err != nil {
		return nil, fmt.Errorf("decode WAV: %w", err)
	}
	bits := int(dec.BitDepth)

	switch dec.WavAudioFormat {
	case wavFormatPCM, 0:
		// 8-bit WAV is unsigned (0..255 centered on 128); translate to signed
		// before scaleToInt16 (which assumes signed input).
		if bits == 8 {
			for i, v := range buf.Data {
				buf.Data[i] = v - 128
			}
		}
	case wavFormatFloat:
		// IEEE float WAVs (common in DAW bounces) come back from go-audio/wav
		// with the FLOAT BIT PATTERNS packed into int slots — there's an open
		// TODO in their 4-byte decoder. We reinterpret + scale to int16 here.
		if bits != 32 {
			return nil, fmt.Errorf("unsupported float WAV bit depth %d (want 32)", bits)
		}
		for i, v := range buf.Data {
			f32 := math.Float32frombits(uint32(int32(v)))
			// Clamp to [-1,1] (rare DAW bounces exceed this) and scale to int16.
			if f32 > 1.0 {
				f32 = 1.0
			} else if f32 < -1.0 {
				f32 = -1.0
			}
			buf.Data[i] = int(math.Round(float64(f32) * 32767))
		}
		bits = 16 // downstream scaleToInt16 should now treat as signed 16-bit
	default:
		return nil, fmt.Errorf("unsupported WAV audio format 0x%04X", dec.WavAudioFormat)
	}

	return intBufferToLoadedAudio(buf, bits, mode)
}

func loadAIFF(path string, mode ChannelMode) (*LoadedAudio, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := aiff.NewDecoder(f)
	if !dec.IsValidFile() {
		return nil, errors.New("not a valid AIFF file")
	}
	buf, err := dec.FullPCMBuffer()
	if err != nil {
		return nil, fmt.Errorf("decode AIFF: %w", err)
	}
	return intBufferToLoadedAudio(buf, int(dec.BitDepth), mode)
}

// intBufferToLoadedAudio is the format-agnostic part: given a decoded
// audio.IntBuffer of SIGNED samples plus the source bit depth, fold to mono
// int16 per the requested ChannelMode.
func intBufferToLoadedAudio(buf *audio.IntBuffer, bits int, mode ChannelMode) (*LoadedAudio, error) {
	if buf == nil || buf.Format == nil {
		return nil, errors.New("decoder returned no format")
	}
	channels := buf.Format.NumChannels
	if channels < 1 {
		return nil, errors.New("input has zero channels")
	}
	rate := uint32(buf.Format.SampleRate)
	if rate == 0 {
		return nil, errors.New("input has zero sample rate")
	}
	frames := len(buf.Data) / channels

	// pick selects which channel offset (per frame) to sample for left/right;
	// ignored when mode == ChannelMix.
	pick := 0
	if mode == ChannelRight && channels >= 2 {
		pick = 1
	}

	pcm := make([]int16, frames)
	switch {
	case channels == 1:
		for i := 0; i < frames; i++ {
			pcm[i] = clampInt16(scaleToInt16(buf.Data[i], bits))
		}
	case mode == ChannelMix:
		for i := 0; i < frames; i++ {
			var sum int64
			for c := 0; c < channels; c++ {
				sum += int64(scaleToInt16(buf.Data[i*channels+c], bits))
			}
			pcm[i] = clampInt16(int32(sum / int64(channels)))
		}
	default: // ChannelLeft or ChannelRight
		for i := 0; i < frames; i++ {
			pcm[i] = clampInt16(scaleToInt16(buf.Data[i*channels+pick], bits))
		}
	}
	return &LoadedAudio{PCM: pcm, SampleRate: rate}, nil
}

// ResampleTo unconditionally linearly resamples a to targetHz. Used both for
// forcing an out-of-range source into the S950's window and for deliberately
// resampling an in-range source down to a low rate to get the lo-fi aliasing
// character the S950 is known for (10 kHz / 12 kHz are classic).
//
// The target must be inside [MinSampleRateHz, MaxSampleRateHz]; out-of-range
// targets are clamped to the nearest boundary.
//
// The S950 is a 12-bit sampler so simple linear interpolation is more than
// adequate; the quantisation noise floor dominates any resampler artifacts.
// We also do NOT apply an anti-alias filter before decimation — preserving
// aliasing is the point when downsampling for character.
func ResampleTo(a *LoadedAudio, targetHz uint32) *LoadedAudio {
	if a == nil {
		return nil
	}
	target := clampToS950Range(targetHz)
	if target == a.SampleRate {
		return a
	}
	return &LoadedAudio{
		PCM:        linearResample(a.PCM, a.SampleRate, target),
		SampleRate: target,
	}
}

// ResampleToS950Range returns a in the S950's accepted rate window: passes
// through if already in range, otherwise resamples to fallbackHz (clamped to
// range; 0 means default 44.1 kHz).
func ResampleToS950Range(a *LoadedAudio, fallbackHz uint32) *LoadedAudio {
	if a == nil {
		return nil
	}
	if InRange(a.SampleRate) {
		return a
	}
	if fallbackHz == 0 {
		fallbackHz = 44100
	}
	return ResampleTo(a, fallbackHz)
}

// MaxSampleRateHz / MinSampleRateHz are the rates corresponding to the S950's
// MinPeriodNS / MaxPeriodNS limits.
var (
	MaxSampleRateHz = uint32(math.Round(1e9 / float64(MinPeriodNS))) // ~65535 Hz
	MinSampleRateHz = uint32(math.Round(1e9 / float64(MaxPeriodNS))) // 2000 Hz
)

// RateAliases maps short names to sample rates in Hz. Includes the S950's own
// preset rates plus a few iconic 12-bit-era machines for character matching.
var RateAliases = map[string]uint32{
	// S950 native preset rates, low → high. "s950-N" where N is the kHz tens.
	"s950-7":  7500,
	"s950-10": 10000,
	"s950-12": 12000,
	"s950-15": 15000,
	"s950-20": 20000,
	"s950-26": 26040,
	"s950-31": 31250,
	"s950-37": 37500,
	"s950-40": 40000,

	// Other-machine character aliases.
	"telephone": 8000,  // POTS-grade, very gritty
	"lofi":      10000, // generic "very lo-fi"
	"sp1200":    26040, // E-mu SP-1200, the original 12-bit hip-hop sound
	"mpc60":     40000, // Akai MPC60 / MPC60-II
}

// ParseRate accepts either a numeric Hz value ("44100") or a named alias
// ("sp1200", "lofi") and returns the corresponding sample rate. Returns 0
// for the empty string (caller's signal for "use source rate as-is").
func ParseRate(s string) (uint32, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" {
		return 0, nil
	}
	if hz, ok := RateAliases[s]; ok {
		return hz, nil
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid sample rate %q (want a number in Hz or one of: %s)",
			s, sortedAliasNames())
	}
	return uint32(n), nil
}

func sortedAliasNames() string {
	names := make([]string, 0, len(RateAliases))
	for k := range RateAliases {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// InRange reports whether hz can be expressed as a valid S950 period.
func InRange(hz uint32) bool {
	return hz >= MinSampleRateHz && hz <= MaxSampleRateHz
}

func clampToS950Range(hz uint32) uint32 {
	if hz < MinSampleRateHz {
		return MinSampleRateHz
	}
	if hz > MaxSampleRateHz {
		return MaxSampleRateHz
	}
	return hz
}

// linearResample resamples src from srcHz to dstHz using linear interpolation.
// Returns a new slice; src is not modified.
func linearResample(src []int16, srcHz, dstHz uint32) []int16 {
	if srcHz == dstHz || len(src) == 0 {
		out := make([]int16, len(src))
		copy(out, src)
		return out
	}
	ratio := float64(srcHz) / float64(dstHz)
	dstLen := int(math.Round(float64(len(src)) / ratio))
	if dstLen < 1 {
		dstLen = 1
	}
	out := make([]int16, dstLen)
	for i := 0; i < dstLen; i++ {
		srcPos := float64(i) * ratio
		idx := int(srcPos)
		frac := srcPos - float64(idx)
		if idx >= len(src)-1 {
			out[i] = src[len(src)-1]
			continue
		}
		s0 := float64(src[idx])
		s1 := float64(src[idx+1])
		out[i] = clampInt16(int32(math.Round(s0 + (s1-s0)*frac)))
	}
	return out
}
