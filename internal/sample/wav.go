package sample

import (
	"errors"
	"fmt"
	"os"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
)

// LoadedWAV is a mono 16-bit PCM stream ready for S950 upload.
type LoadedWAV struct {
	PCM        []int16
	SampleRate uint32 // Hz
}

// LoadWAV reads a WAV file, downmixes stereo to mono if needed, and returns
// a 16-bit signed PCM buffer plus the sample rate. Non-16-bit input is scaled.
func LoadWAV(path string) (*LoadedWAV, error) {
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
	if buf.Format == nil {
		return nil, errors.New("WAV has no format")
	}
	channels := buf.Format.NumChannels
	if channels < 1 {
		return nil, errors.New("WAV has zero channels")
	}
	rate := uint32(buf.Format.SampleRate)
	bits := dec.BitDepth
	frames := len(buf.Data) / channels

	pcm := make([]int16, frames)
	switch {
	case channels == 1:
		for i := 0; i < frames; i++ {
			pcm[i] = clampInt16(scaleToInt16(buf.Data[i], int(bits)))
		}
	default:
		// Downmix to mono by averaging channels.
		for i := 0; i < frames; i++ {
			var sum int64
			for c := 0; c < channels; c++ {
				sum += int64(scaleToInt16(buf.Data[i*channels+c], int(bits)))
			}
			pcm[i] = clampInt16(int32(sum / int64(channels)))
		}
	}
	return &LoadedWAV{PCM: pcm, SampleRate: rate}, nil
}

// SaveWAV writes a 16-bit mono WAV file from PCM and a sample rate.
func SaveWAV(path string, pcm []int16, sampleRate uint32) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := wav.NewEncoder(f, int(sampleRate), 16, 1, 1)
	intBuf := &audio.IntBuffer{
		Format:         &audio.Format{NumChannels: 1, SampleRate: int(sampleRate)},
		SourceBitDepth: 16,
		Data:           make([]int, len(pcm)),
	}
	for i, s := range pcm {
		intBuf.Data[i] = int(s)
	}
	if err := enc.Write(intBuf); err != nil {
		return fmt.Errorf("WAV encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("WAV close: %w", err)
	}
	return nil
}

// scaleToInt16 normalises a decoded integer sample (whose dynamic range is set
// by the source bit depth) to signed 16-bit.
func scaleToInt16(v int, srcBits int) int32 {
	switch {
	case srcBits == 16:
		return int32(int16(v))
	case srcBits == 8:
		// 8-bit WAV is unsigned (0..255 centred on 128).
		return int32((v - 128) << 8)
	case srcBits == 24:
		// 24-bit signed; sign-extend then drop 8 bits.
		x := int32(v)
		if x&0x800000 != 0 {
			x |= ^0xFFFFFF
		}
		return x >> 8
	case srcBits == 32:
		return int32(v >> 16)
	default:
		// Best-effort scale.
		max := int64(1) << uint(srcBits-1)
		if max == 0 {
			return int32(v)
		}
		return int32((int64(v) * 32767) / max)
	}
}

func clampInt16(v int32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}
