// Command s950 is a CLI for talking to an Akai S900/S950 sampler over MIDI.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/sample"
	"github.com/bivers/s950/internal/transport"
)

// Global flags (populated by cobra in PersistentPreRunE).
var (
	flagIn      string
	flagOut     string
	flagChannel int
	flagVerbose bool
	flagTimeout time.Duration
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "s950",
		Short: "Akai S900/S950 sampler MIDI control CLI",
		Long: "s950 is a command-line tool for managing samples and parameters on an\n" +
			"Akai S900/S950 sampler over a standard MIDI connection.",
		SilenceUsage: true,
	}

	rootCmd.PersistentFlags().StringVar(&flagIn, "in", "",
		"MIDI input port (substring match; first port if empty)")
	rootCmd.PersistentFlags().StringVar(&flagOut, "out", "",
		"MIDI output port (substring match; first port if empty)")
	rootCmd.PersistentFlags().IntVar(&flagChannel, "channel", 0,
		"S950 MIDI channel (0..15)")
	rootCmd.PersistentFlags().BoolVar(&flagVerbose, "verbose", false,
		"hex-dump every SysEx exchanged with the device to stderr")
	rootCmd.PersistentFlags().DurationVar(&flagTimeout, "timeout", 5*time.Second,
		"per-request timeout")

	rootCmd.AddCommand(
		newPortsCmd(),
		newCatalogCmd(),
		newGetParamsCmd(),
		newPutSampleCmd(),
		newMonitorCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// openTransport opens MIDI in/out using the global flags.
func openTransport() (*transport.Transport, error) {
	opts := transport.Options{
		In:      flagIn,
		Out:     flagOut,
		Verbose: flagVerbose,
		LogFunc: func(format string, args ...interface{}) {
			fmt.Fprintf(os.Stderr, format, args...)
		},
	}
	return transport.Open(opts)
}

// newDevice opens the transport and wraps it in a Device.
func newDevice() (*device.Device, func(), error) {
	t, err := openTransport()
	if err != nil {
		return nil, nil, err
	}
	d := device.New(t, byte(flagChannel))
	d.RequestTimeout = flagTimeout
	cleanup := func() { _ = t.Close() }
	return d, cleanup, nil
}

func newPortsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ports",
		Short: "List available MIDI input/output ports",
		RunE: func(cmd *cobra.Command, args []string) error {
			ins, outs, err := transport.ListPorts()
			if err != nil {
				return err
			}
			fmt.Println("MIDI inputs:")
			for _, p := range ins {
				fmt.Printf("  [%d] %s\n", p.Index, p.Name)
			}
			fmt.Println("MIDI outputs:")
			for _, p := range outs {
				fmt.Printf("  [%d] %s\n", p.Index, p.Name)
			}
			return nil
		},
	}
}

func newCatalogCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "catalog",
		Short: "List samples and programs on the device",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()

			entries, err := d.Catalog()
			if err != nil {
				return err
			}
			fmt.Printf("%-4s %-4s %s\n", "TYPE", "NUM", "NAME")
			for _, e := range entries {
				typeName := "?"
				switch e.Type {
				case 'P':
					typeName = "PRG"
				case 'S':
					typeName = "SMP"
				}
				fmt.Printf("%-4s %-4d %s\n", typeName, e.Num, e.Name)
			}
			return nil
		},
	}
}

func newGetParamsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get-params <sample-num>",
		Short: "Read sample parameters from the device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			num, err := parseSampleNum(args[0])
			if err != nil {
				return err
			}
			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()

			p, err := d.GetParams(num)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(params2JSON(p))
			}
			printParams(p, num)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

func newPutSampleCmd() *cobra.Command {
	var slot int
	var loopStart, loopEnd uint32
	var mode int
	var maxFrames int
	var channelStr string
	var rateStr string
	var tuneSemitones float64
	cmd := &cobra.Command{
		Use:   "put-sample <in.wav|in.aiff>",
		Short: "Upload an audio file (WAV or AIFF) to the device",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			chMode, err := sample.ParseChannelMode(channelStr)
			if err != nil {
				return err
			}
			storeRate, err := sample.ParseRate(rateStr)
			if err != nil {
				return err
			}
			a, err := sample.LoadAudio(path, chMode)
			if err != nil {
				return fmt.Errorf("load audio: %w", err)
			}
			fmt.Fprintf(os.Stderr, "loaded %d frames @ %dHz\n", len(a.PCM), a.SampleRate)

			switch {
			case storeRate != 0:
				// Explicit user-chosen storage rate: always resample to it.
				if !sample.InRange(storeRate) {
					return fmt.Errorf("--rate %d is outside S950 range (%d..%d Hz)",
						storeRate, sample.MinSampleRateHz, sample.MaxSampleRateHz)
				}
				if storeRate != a.SampleRate {
					orig := a.SampleRate
					a = sample.ResampleTo(a, storeRate)
					fmt.Fprintf(os.Stderr, "resampled %dHz -> %dHz (%d frames)\n",
						orig, a.SampleRate, len(a.PCM))
				}
			case !sample.InRange(a.SampleRate):
				// Source out of range and no explicit --rate: fall back to 44.1k.
				orig := a.SampleRate
				a = sample.ResampleToS950Range(a, 0)
				fmt.Fprintf(os.Stderr, "source %dHz out of S950 range; resampled to %dHz (%d frames)\n",
					orig, a.SampleRate, len(a.PCM))
			}

			if maxFrames > 0 && maxFrames < len(a.PCM) {
				a.PCM = a.PCM[:maxFrames]
				fmt.Fprintf(os.Stderr, "truncated to %d frames\n", len(a.PCM))
			}

			words, err := sample.PCM16ToWords(a.PCM)
			if err != nil {
				return fmt.Errorf("convert: %w", err)
			}

			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()

			// Auto-pick a slot if the requested one is occupied. Overwriting
			// an existing sample on the S950 NAKs every block, truncating
			// the new upload — so we always need a clean target.
			//
			// Fail-soft: if the catalog can't be read (S950 stuck after a
			// prior NAK incident, etc.) fall through with the requested
			// slot. The NAK detector after the dump will still surface any
			// real overwrite-induced failure.
			finalSlot := byte(slot)
			picked, changed, perr := d.PickSlot(byte(slot))
			if perr != nil {
				fmt.Fprintf(os.Stderr, "warning: could not read catalog to auto-pick slot (%v); using --slot %d as-is\n", perr, slot)
			} else {
				finalSlot = picked
				if changed {
					fmt.Fprintf(os.Stderr, "slot %d is occupied; using slot %d instead\n",
						slot, finalSlot)
				}
			}

			opts := device.PutSampleOpts{
				Num:          finalSlot,
				SampleRateHz: a.SampleRate,
				LoopStart:    loopStart,
				LoopEnd:      loopEnd,
				Mode:         byte(mode),
			}
			start := time.Now()
			sent, drain, err := d.PutSampleOpenLoop(words, opts)
			if err != nil {
				return fmt.Errorf("upload: %w", err)
			}
			fmt.Fprintf(os.Stderr, "queued %d bytes to slot %d; waiting %s for MIDI to drain...\n",
				sent, finalSlot, drain.Round(100*time.Millisecond))
			// rtmidi/CoreMIDI buffers asynchronously; we have to wait for the
			// OS to actually clock the bytes out at 31250 baud.
			waitWithProgress(drain)

			// Listen for NAKs the S950 may have emitted during the upload.
			// Any NAK means a block failed checksum on the device side and
			// the stored sample is partial — the upload is unrecoverable in
			// open-loop mode and must be retried.
			naks := d.CollectNAKs(500 * time.Millisecond)
			if naks > 0 {
				return fmt.Errorf("upload received %d NAK(s) from the S950 — sample may be truncated; please retry put-sample", naks)
			}
			fmt.Fprintf(os.Stderr, "upload accepted (no NAKs); drain done in %s\n",
				time.Since(start).Round(time.Millisecond))

			// SPRM rewrite only when there's something explicit to set —
			// otherwise leave the S950's auto-generated parameters alone.
			needsSPRM := tuneSemitones != 0 || loopStart != 0 || loopEnd != 0
			if needsSPRM {
				time.Sleep(200 * time.Millisecond)
				p, err := d.GetParams(finalSlot)
				if err != nil {
					return fmt.Errorf("read SPRM: %w", err)
				}
				if tuneSemitones != 0 {
					// SNOMP is the keyboard note at which the sample plays at
					// its native rate ("home pitch"). LOWERING SNOMP shifts
					// home down, so playing the same key sounds HIGHER. To
					// match intuitive DAW semantics (--tune +12 = one octave
					// up), we therefore SUBTRACT N*16 from SNOMP for +N
					// semitones up. Units are 1/16 semitone.
					pitch := int32(p.NominalPitch) - int32(tuneSemitones*16)
					if pitch < 0 {
						pitch = 0
					}
					if pitch > 0xFFFF {
						pitch = 0xFFFF
					}
					p.NominalPitch = uint16(pitch)
					fmt.Fprintf(os.Stderr, "tuned %+.2f semitones (SNOMP %d)\n",
						tuneSemitones, p.NominalPitch)
				}
				if loopStart != 0 || loopEnd != 0 {
					p.Start = loopStart
					p.End = loopEnd
				}
				if err := d.SetParams(finalSlot, p); err != nil {
					return fmt.Errorf("write SPRM: %w", err)
				}
				// Verify by reading back immediately on the same connection.
				time.Sleep(200 * time.Millisecond)
				v, verr := d.GetParams(finalSlot)
				if verr != nil {
					fmt.Fprintf(os.Stderr, "warning: SPRM verify read failed: %v\n", verr)
				} else {
					fmt.Fprintf(os.Stderr, "verified SPRM on device: pitch=%d end=%d loop=%d\n",
						v.NominalPitch, v.End, v.LoopLength)
				}
			}
			fmt.Fprintf(os.Stderr, "done in %s\n", time.Since(start).Round(time.Millisecond))
			return nil
		},
	}
	cmd.Flags().IntVar(&slot, "slot", 0, "destination sample slot on the S950 (0..99)")
	cmd.Flags().Uint32Var(&loopStart, "loop-start", 0, "loop start (words)")
	cmd.Flags().Uint32Var(&loopEnd, "loop-end", 0, "loop end (words); when <= loop-start+5, sample is one-shot")
	cmd.Flags().IntVar(&mode, "mode", 0, "mode: 0=looping, 1=alternating")
	cmd.Flags().IntVar(&maxFrames, "max-frames", 0, "truncate input to N frames (0 = no limit)")
	cmd.Flags().StringVar(&channelStr, "channel-mode", "mix",
		"how to fold stereo to mono: left|right|mix")
	cmd.Flags().StringVar(&rateStr, "rate", "",
		"force-resample to this rate before upload — accepts a number in Hz\n"+
			"(e.g. 10000) or a named alias for lo-fi character matching.\n"+
			"Aliases: telephone(8k), lofi(10k), sp1200(26040), mpc60(40k),\n"+
			"and s950-7/10/12/15/20/26/31/37/40 for the S950's native presets.\n"+
			"Empty = keep source rate (resample only if out of S950 range)")
	cmd.Flags().Float64Var(&tuneSemitones, "tune", 0,
		"transpose the sample's nominal pitch by N semitones (can be negative\n"+
			"or fractional, e.g. -12 = down one octave, +7 = up a fifth). The\n"+
			"audio data is unchanged; the S950 plays back from a different key\n"+
			"as the new \"home\" pitch")
	return cmd
}

func newMonitorCmd() *cobra.Command {
	var duration time.Duration
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Listen for inbound SysEx on the input port and hex-dump everything",
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := openTransport()
			if err != nil {
				return err
			}
			defer t.Close()

			fmt.Fprintf(os.Stderr, "listening on %q for %s (Ctrl-C to stop)...\n",
				t.InName(), duration)
			ctx, cancel := signal.NotifyContext(context.Background(),
				os.Interrupt, syscall.SIGTERM)
			defer cancel()
			deadline := time.Now().Add(duration)
			count := 0
			for time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return nil
				default:
				}
				left := time.Until(deadline)
				if left > time.Second {
					left = time.Second
				}
				msg, err := t.RecvSysEx(left)
				if err != nil {
					continue
				}
				count++
				fmt.Printf("[%d] (%d bytes) % X\n", count, len(msg), msg)
			}
			fmt.Fprintf(os.Stderr, "received %d SysEx messages\n", count)
			return nil
		},
	}
	cmd.Flags().DurationVar(&duration, "duration", 10*time.Second,
		"how long to listen before quitting")
	return cmd
}

// waitWithProgress sleeps for d, printing a dot every second so long
// transfers don't look hung.
func waitWithProgress(d time.Duration) {
	if d <= time.Second {
		time.Sleep(d)
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		<-tick.C
		fmt.Fprintf(os.Stderr, ".")
	}
	fmt.Fprintln(os.Stderr)
}

func parseSampleNum(s string) (byte, error) {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, fmt.Errorf("invalid sample number %q: %w", s, err)
	}
	if n < 0 || n > 99 {
		return 0, fmt.Errorf("sample number %d out of S950 range 0..99", n)
	}
	return byte(n), nil
}

func printParams(p *protocol.SampleParams, num byte) {
	fmt.Printf("Sample %d: %q\n", num, p.Name)
	fmt.Printf("  total words : %d\n", p.TotalWords)
	fmt.Printf("  sample rate : %d Hz\n", p.SampleRateHz)
	fmt.Printf("  nominal pch : %d (C3=960; 1/16-semitone)\n", p.NominalPitch)
	fmt.Printf("  loud offset : %d\n", p.LoudOffset)
	fmt.Printf("  replay mode : %q\n", p.ReplayMode)
	fmt.Printf("  start       : %d\n", p.Start)
	fmt.Printf("  end         : %d\n", p.End)
	fmt.Printf("  loop length : %d\n", p.LoopLength)
	fmt.Printf("  reversed    : %q\n", p.Reversed)
	if p.VelXFade != 0 {
		fmt.Printf("  velocity xf : on\n")
	}
}

// params2JSON returns a JSON-serialisable view of SampleParams.
func params2JSON(p *protocol.SampleParams) map[string]any {
	return map[string]any{
		"name":            p.Name,
		"total_words":     p.TotalWords,
		"sample_rate_hz":  p.SampleRateHz,
		"nominal_pitch":   p.NominalPitch,
		"loudness_offset": p.LoudOffset,
		"replay_mode":     string([]byte{p.ReplayMode}),
		"start":           p.Start,
		"end":             p.End,
		"loop_length":     p.LoopLength,
		"reversed":        string([]byte{p.Reversed}),
		"velocity_xfade":  p.VelXFade != 0,
	}
}
