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
		newGetSampleCmd(),
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

func newGetSampleCmd() *cobra.Command {
	var outPath string
	var rawPath string
	var ackStrategy string
	var ackInterval time.Duration
	cmd := &cobra.Command{
		Use:   "get-sample <sample-num> <out.wav>",
		Short: "Download a sample to a WAV file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			num, err := parseSampleNum(args[0])
			if err != nil {
				return err
			}
			outPath = args[1]

			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()

			ctx, cancel := signal.NotifyContext(context.Background(),
				os.Interrupt, syscall.SIGTERM)
			defer cancel()

			d.DumpTimeout = 10 * time.Minute
			d.AckPaceInterval = ackInterval
			fmt.Fprintf(os.Stderr, "downloading sample %d (this can take minutes)...\n", num)
			var sink func([]byte)
			if rawPath != "" {
				sink = func(raw []byte) {
					if werr := os.WriteFile(rawPath, raw, 0644); werr != nil {
						fmt.Fprintf(os.Stderr, "warning: could not write raw dump to %s: %v\n", rawPath, werr)
					} else {
						fmt.Fprintf(os.Stderr, "wrote raw dump to %s (%d bytes)\n", rawPath, len(raw))
					}
				}
			}
			var dumpOpts device.GetSampleOpts
			switch ackStrategy {
			case "pump":
				// default
			case "single":
				dumpOpts.SingleAck = true
			case "none":
				dumpOpts.NoAckPump = true
			default:
				return fmt.Errorf("unknown --ack mode %q (use pump|single|none)", ackStrategy)
			}
			dump, err := d.GetSample(ctx, num, dumpOpts, sink)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "got %d words, period=%dns (%dHz), loop %d..%d, mode=%d\n",
				len(dump.Words), dump.Header.PeriodNS,
				sample.PeriodNSToHz(dump.Header.PeriodNS),
				dump.Header.LoopStart, dump.Header.LoopEnd, dump.Header.Mode)

			pcm := sample.WordsToPCM16(dump.Words)
			rate := sample.PeriodNSToHz(dump.Header.PeriodNS)
			if err := sample.SaveWAV(outPath, pcm, rate); err != nil {
				return fmt.Errorf("save WAV: %w", err)
			}
			fmt.Printf("wrote %s\n", outPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&rawPath, "raw", "",
		"also write the unprocessed SysEx bytes to this path (for debugging)")
	cmd.Flags().StringVar(&ackStrategy, "ack", "pump",
		"ACK strategy: pump (continuous, default), single (one ACK only), none (no ACKs)")
	cmd.Flags().DurationVar(&ackInterval, "ack-interval", 50*time.Millisecond,
		"interval between ACKs when --ack=pump (empirically optimal: 50ms)")
	return cmd
}

func newPutSampleCmd() *cobra.Command {
	var slot int
	var loopStart, loopEnd uint32
	var mode int
	var maxFrames int
	cmd := &cobra.Command{
		Use:   "put-sample <in.wav>",
		Short: "Upload a WAV file to the device (open-loop)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			w, err := sample.LoadWAV(path)
			if err != nil {
				return fmt.Errorf("load WAV: %w", err)
			}
			fmt.Fprintf(os.Stderr, "loaded %d frames @ %dHz\n", len(w.PCM), w.SampleRate)
			if maxFrames > 0 && maxFrames < len(w.PCM) {
				w.PCM = w.PCM[:maxFrames]
				fmt.Fprintf(os.Stderr, "truncated to %d frames\n", len(w.PCM))
			}

			words, err := sample.PCM16ToWords(w.PCM)
			if err != nil {
				return fmt.Errorf("convert: %w", err)
			}

			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()

			opts := device.PutSampleOpts{
				Num:          byte(slot),
				SampleRateHz: w.SampleRate,
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
				sent, slot, drain.Round(100*time.Millisecond))
			// rtmidi/CoreMIDI buffers asynchronously; we have to wait for the
			// OS to actually clock the bytes out at 31250 baud.
			waitWithProgress(drain)
			fmt.Fprintf(os.Stderr, "done in %s\n", time.Since(start).Round(time.Millisecond))
			return nil
		},
	}
	cmd.Flags().IntVar(&slot, "slot", 0, "destination sample slot on the S950 (0..99)")
	cmd.Flags().Uint32Var(&loopStart, "loop-start", 0, "loop start (words)")
	cmd.Flags().Uint32Var(&loopEnd, "loop-end", 0, "loop end (words); when <= loop-start+5, sample is one-shot")
	cmd.Flags().IntVar(&mode, "mode", 0, "mode: 0=looping, 1=alternating")
	cmd.Flags().IntVar(&maxFrames, "max-frames", 0, "truncate input to N frames (0 = no limit)")
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
