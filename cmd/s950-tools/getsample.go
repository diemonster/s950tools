// CLI subcommand for `s950-tools get-sample` — the tight diagnostic
// loop for Phase 1C closed-loop receive. Has the GUI's "Copy from
// S950" out of the picture so we can iterate on the wire-level
// handshake without the modal + Wails-rebuild overhead.
//
// With --verbose the underlying transport hex-dumps every inbound /
// outbound SysEx to stderr, so a single run shows the full TX/RX
// trace inline. Edit BuildS950Handshake (or recvClosedLoopBlocks)
// and re-run to A/B different ACK shapes.

package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/spf13/cobra"

	"github.com/bivers/s950/internal/sample"
)

func newGetSampleCmd() *cobra.Command {
	var (
		dumpTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "get-sample <slot> [out.wav]",
		Short: "Request a sample dump (RSD) from the S950 and decode it",
		Long: "Sends an RSD (Request Sample Dump) to the device for the given slot,\n" +
			"reads back the SDS envelope(s), reconstructs the 12-bit words, and\n" +
			"optionally writes a 16-bit WAV at the device's sample rate.\n\n" +
			"Useful as a tight diagnostic loop for closed-loop receive — pair with\n" +
			"--verbose to hex-dump every SysEx exchanged. Edit the closed-loop\n" +
			"handshake builder (protocol.BuildS950Handshake) or the recv state\n" +
			"machine (device.recvClosedLoopBlocks) and re-run without rebuilding\n" +
			"the GUI.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			slot64, err := strconv.ParseUint(args[0], 10, 8)
			if err != nil {
				return fmt.Errorf("parse slot: %w", err)
			}
			slot := byte(slot64)
			if slot > 99 {
				return fmt.Errorf("slot %d out of range (0..99)", slot)
			}

			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()

			fmt.Fprintf(os.Stderr, "requesting sample dump for slot %d (timeout %s)...\n",
				slot, dumpTimeout)
			start := time.Now()
			hdr, words, err := d.GetSampleAudio(slot, dumpTimeout)
			elapsed := time.Since(start)
			if err != nil {
				return fmt.Errorf("GetSampleAudio: %w (after %s)", err, elapsed)
			}

			rateHz := sample.PeriodNSToHz(hdr.PeriodNS)
			fmt.Fprintf(os.Stderr,
				"received %d words in %s (%.2f kHz, %.2fs of audio)\n",
				len(words), elapsed,
				float64(rateHz)/1000.0,
				float64(len(words))/float64(rateHz))
			fmt.Fprintf(os.Stderr,
				"  loop start: %d, loop end: %d, mode: %d\n",
				hdr.LoopStart, hdr.LoopEnd, hdr.Mode)

			if len(args) < 2 {
				return nil
			}
			out := args[1]
			if err := writeSampleWAV(out, words, rateHz); err != nil {
				return fmt.Errorf("write %s: %w", out, err)
			}
			fmt.Fprintf(os.Stderr, "wrote %s\n", out)
			return nil
		},
	}
	cmd.Flags().DurationVar(&dumpTimeout, "dump-timeout", 10*time.Minute,
		"overall ceiling for the dump receive — caps the per-block timeout's accumulated wait")
	return cmd
}

// writeSampleWAV writes a 16-bit mono WAV at the device's reported
// sample rate. The 12-bit S950 words are sign-extended to int16 via
// sample.SWtoPCM16 (the same conversion the host uses when importing
// device audio into the GUI).
func writeSampleWAV(path string, words []uint16, rateHz uint32) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := wav.NewEncoder(f, int(rateHz), 16, 1, 1)
	buf := &audio.IntBuffer{
		Format:         &audio.Format{NumChannels: 1, SampleRate: int(rateHz)},
		SourceBitDepth: 16,
		Data:           make([]int, len(words)),
	}
	for i, w := range words {
		buf.Data[i] = int(sample.SWtoPCM16(w))
	}
	if err := enc.Write(buf); err != nil {
		return err
	}
	return enc.Close()
}
