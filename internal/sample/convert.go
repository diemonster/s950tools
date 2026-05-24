// Package sample implements lossless-where-possible conversion between PCM
// audio (as in a WAV file) and the S950's 12-bit offset-binary format.
package sample

import (
	"errors"
	"math"
)

// S950 sample-length limits, per the reference doc.
const (
	MinTotalWords uint32 = 200
	MaxTotalWords uint32 = 475020
)

// S950 sampling-period range, in nanoseconds (~2 kHz..65.5 kHz).
const (
	MinPeriodNS uint32 = 15259
	MaxPeriodNS uint32 = 500000
)

// SilenceWord is the S950's offset-binary value for true silence (12-bit
// midpoint, 0x800).
const SilenceWord uint16 = 0x800

// ErrSampleRate indicates a sample rate outside the S950's accepted range
// (approximately 2 kHz to 65.5 kHz).
var ErrSampleRate = errors.New("sample rate outside S950 range (~2kHz..65.5kHz)")

// ErrLengthTooShort indicates fewer than MinTotalWords samples.
var ErrLengthTooShort = errors.New("sample too short for S950 (min 200 words)")

// ErrLengthTooLong indicates more than MaxTotalWords samples.
var ErrLengthTooLong = errors.New("sample too long for S950 (max 475020 words)")

// PCM16toSW maps a signed 16-bit PCM sample to a 12-bit offset-binary S950 word.
// 16→12 conversion is an arithmetic right shift by 4; silence then sits at 0x800.
func PCM16toSW(s int16) uint16 {
	v := int32(s) >> 4 // signed -> 12-bit signed
	v += 2048          // offset binary: silence = 0x800
	if v < 0 {
		v = 0
	}
	if v > 4095 {
		v = 4095
	}
	return uint16(v)
}

// SWtoPCM16 reverses PCM16toSW (with the obvious 4-bit precision loss).
func SWtoPCM16(w uint16) int16 {
	return int16((int32(w&0x0FFF) - 2048) << 4)
}

// HzToPeriodNS converts a sample rate in Hz to the nanosecond period the S950
// stores in the dump header. Returns ErrSampleRate if the result is outside
// the S950's accepted period range.
func HzToPeriodNS(hz uint32) (uint32, error) {
	if hz == 0 {
		return 0, ErrSampleRate
	}
	p := uint32(math.Round(1e9 / float64(hz)))
	if p < MinPeriodNS || p > MaxPeriodNS {
		return 0, ErrSampleRate
	}
	return p, nil
}

// PeriodNSToHz reverses HzToPeriodNS.
func PeriodNSToHz(ns uint32) uint32 {
	if ns == 0 {
		return 0
	}
	return uint32(math.Round(1e9 / float64(ns)))
}

// PCM16ToWords converts a mono 16-bit PCM slice to S950 12-bit words and
// validates the length against S950 limits.
func PCM16ToWords(pcm []int16) ([]uint16, error) {
	if uint32(len(pcm)) < MinTotalWords {
		return nil, ErrLengthTooShort
	}
	if uint32(len(pcm)) > MaxTotalWords {
		return nil, ErrLengthTooLong
	}
	out := make([]uint16, len(pcm))
	for i, s := range pcm {
		out[i] = PCM16toSW(s)
	}
	return out, nil
}

// WordsToPCM16 converts S950 words back to 16-bit signed PCM.
func WordsToPCM16(words []uint16) []int16 {
	out := make([]int16, len(words))
	for i, w := range words {
		out[i] = SWtoPCM16(w)
	}
	return out
}
