// Command s950-tools is a CLI for talking to an Akai S900/S950 sampler over MIDI.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	flagIn          string
	flagOut         string
	flagChannel     int
	flagVerbose     bool
	flagTimeout     time.Duration
	flagSerialPort  string
	flagSerialBaud  int
	flagSerialFlow  string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "s950-tools",
		Short: "Akai S900/S950 sampler MIDI control CLI",
		Long: "s950-tools is a command-line tool for managing samples and parameters on\n" +
			"an Akai S900/S950 sampler over a standard MIDI connection.",
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
	rootCmd.PersistentFlags().StringVar(&flagSerialPort, "rs232", "",
		"open RS-232 serial port instead of MIDI (e.g. /dev/cu.usbserial-XXXX). "+
			"S950 must have controller-select set to RS-232C in overall settings")
	rootCmd.PersistentFlags().IntVar(&flagSerialBaud, "baud", 38400,
		"serial baud rate (must match S950's overall-settings baud)")
	rootCmd.PersistentFlags().StringVar(&flagSerialFlow, "flow", "none",
		"serial flow control: none | rtscts")

	rootCmd.AddCommand(
		newPortsCmd(),
		newCatalogCmd(),
		newGetParamsCmd(),
		newGetProgramCmd(),
		newPutProgramCmd(),
		newProgramTemplateCmd(),
		newPutSampleCmd(),
		newGetSampleCmd(),
		newMonitorCmd(),
		newSetBaudCmd(),
		newGetOverallCmd(),
		newSetOverallCmd(),
		newGetDrumCmd(),
		newSetDrumCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// openTransport opens the user's chosen backend (MIDI by default, or
// RS-232 when --rs232 is set) and returns it through the same
// Transport interface so the device layer is backend-agnostic.
func openTransport() (transport.Transport, error) {
	logf := func(format string, args ...interface{}) {
		fmt.Fprintf(os.Stderr, format, args...)
	}
	if flagSerialPort != "" {
		return transport.OpenSerial(transport.SerialOptions{
			Port:        flagSerialPort,
			Baud:        flagSerialBaud,
			FlowControl: flagSerialFlow,
			Verbose:     flagVerbose,
			LogFunc:     logf,
		})
	}
	return transport.Open(transport.Options{
		In:      flagIn,
		Out:     flagOut,
		Verbose: flagVerbose,
		LogFunc: logf,
	})
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
		Short: "List available MIDI and RS-232 ports",
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
			ser, err := transport.ListSerialPorts()
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: list serial ports: %v\n", err)
				return nil
			}
			fmt.Println("Serial (RS-232):")
			if len(ser) == 0 {
				fmt.Println("  (none)")
			}
			for _, p := range ser {
				fmt.Printf("  %s\n", p)
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

func newGetProgramCmd() *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "get-program <program-num>",
		Short: "Read a program (header + keygroups) from the device as JSON",
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
			d.RequestTimeout = 8 * time.Second // programs are bigger than catalog

			p, err := d.GetProgram(num)
			if err != nil {
				return err
			}
			j := p.ToJSON()
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if outPath != "" {
				f, err := os.Create(outPath)
				if err != nil {
					return fmt.Errorf("create %s: %w", outPath, err)
				}
				defer f.Close()
				enc = json.NewEncoder(f)
				enc.SetIndent("", "  ")
				if err := enc.Encode(&j); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "wrote %s\n", outPath)
				return nil
			}
			return enc.Encode(&j)
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "",
		"write JSON to this file instead of stdout")
	return cmd
}

func newPutProgramCmd() *cobra.Command {
	var slot int
	var skipVerify bool
	var forceSamples bool
	cmd := &cobra.Command{
		Use:   "put-program <file.json>",
		Short: "Write a program (and any bundled samples) to the device from JSON",
		Long: "Writes a program JSON to a slot. If the JSON contains a `samples` array,\n" +
			"each listed audio file is uploaded first (auto-picking empty slots),\n" +
			"with the SPRM Name set to the declared `name` so keygroups can\n" +
			"reference it. Samples whose `name` already exists in the device\n" +
			"catalog are skipped (use --force-samples to re-upload anyway).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			buf, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			var pj protocol.ProgramJSON
			if err := json.Unmarshal(buf, &pj); err != nil {
				return fmt.Errorf("parse JSON: %w", err)
			}
			p := &protocol.Program{}
			if err := p.FromJSON(&pj); err != nil {
				return fmt.Errorf("decode program: %w", err)
			}

			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()

			// --- Sample manifest, if any ---
			if len(pj.Samples) > 0 {
				kitDir := filepath.Dir(path)
				existing := map[string]bool{}
				if cat, err := d.Catalog(); err == nil {
					for _, e := range cat {
						if e.Type == 'S' {
							existing[e.Name] = true
						}
					}
				} else {
					fmt.Fprintf(os.Stderr, "warning: catalog read failed (%v); proceeding without skip-if-present\n", err)
				}

				for i, spec := range pj.Samples {
					name := spec.Name
					if name == "" {
						base := filepath.Base(spec.File)
						name = strings.ToUpper(strings.TrimSuffix(base, filepath.Ext(base)))
						if len(name) > 10 {
							name = name[:10]
						}
					}
					if existing[name] && !forceSamples {
						fmt.Fprintf(os.Stderr, "[%d/%d] %s: already on device, skipping\n",
							i+1, len(pj.Samples), name)
						continue
					}
					filePath := spec.File
					if !filepath.IsAbs(filePath) {
						filePath = filepath.Join(kitDir, filePath)
					}
					fmt.Fprintf(os.Stderr, "[%d/%d] %s ← %s\n",
						i+1, len(pj.Samples), name, filePath)
					if _, err := uploadSample(d, uploadSampleOpts{
						Path:          filePath,
						Name:          name,
						Rate:          spec.Rate,
						ChannelMode:   spec.ChannelMode,
						TuneSemitones: spec.Tune,
						MaxFrames:     spec.MaxFrames,
						LoopStart:     spec.LoopStart,
						LoopEnd:       spec.LoopEnd,
						Mode:          spec.Mode,
						PreferredSlot: 0, // lowest empty
					}); err != nil {
						return fmt.Errorf("sample %q: %w", name, err)
					}
				}
			}

			// --- Program ---
			d.T.Drain()
			start := time.Now()
			if err := d.SetProgram(byte(slot), p); err != nil {
				return fmt.Errorf("write program: %w", err)
			}
			naks := d.CollectNAKs(500 * time.Millisecond)
			if naks > 0 {
				return fmt.Errorf("write received %d NAK(s) — program may not have stored cleanly; please retry (and consider power-cycling the S950 if the target slot was already populated)", naks)
			}
			fmt.Fprintf(os.Stderr, "wrote program %q (%d keygroup%s) to slot %d in %s\n",
				p.Name, len(p.Keygroups), plural(len(p.Keygroups)), slot,
				time.Since(start).Round(time.Millisecond))

			if !skipVerify {
				time.Sleep(200 * time.Millisecond)
				v, err := d.GetProgram(byte(slot))
				if err != nil {
					return fmt.Errorf("verify (get-program): %w", err)
				}
				fmt.Fprintf(os.Stderr, "verified: name=%q keygroups=%d\n",
					v.Name, len(v.Keygroups))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&slot, "slot", 0, "destination program slot (0..99)")
	cmd.Flags().BoolVar(&skipVerify, "no-verify", false,
		"skip the get-program verification read after writing")
	cmd.Flags().BoolVar(&forceSamples, "force-samples", false,
		"re-upload samples even when a sample of the same name already exists on the device")
	return cmd
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// uploadSampleOpts is the input to uploadSample — the shared orchestration
// used by both put-sample (CLI flags) and put-program (kit JSON manifest).
//
// Pitch model: we follow dxzl/akai-s950's reference behavior — leave SNOMP at
// the S950's default (960 = C3) on upload, and let the user adjust pitch via
// the --tune flag (or the program JSON's per-keygroup soft_tune field). The
// S950's actual rate↔pitch math interacts with multiple opaque variables
// (control_bits flags, internal reference rate, etc.), and the standard
// community practice is to tune by ear rather than try to compute it.
type uploadSampleOpts struct {
	Path          string
	Name          string  // S950 SPRM name to set after upload (empty = leave default)
	Rate          string  // "" | "10000" | "sp1200" etc.
	ChannelMode   string  // "" | "left" | "right" | "mix"
	TuneSemitones float64 // ± semitones applied to SNOMP (1/16 precision)
	MaxFrames     int
	LoopStart     uint32
	LoopEnd       uint32
	Mode          byte
	PreferredSlot int // auto-picks lowest empty if occupied
}

// uploadSample loads an audio file, resamples + folds to mono per opts,
// uploads to a slot on the device, verifies via NAK detection, and writes the
// SPRM (sample name / tune / loop) when any of those is set.
//
// Returns the slot it actually landed in.
func uploadSample(d *device.Device, o uploadSampleOpts) (byte, error) {
	chMode, err := sample.ParseChannelMode(o.ChannelMode)
	if err != nil {
		return 0, err
	}
	storeRate, err := sample.ParseRate(o.Rate)
	if err != nil {
		return 0, err
	}
	a, err := sample.LoadAudio(o.Path, chMode)
	if err != nil {
		return 0, fmt.Errorf("load %s: %w", o.Path, err)
	}
	fmt.Fprintf(os.Stderr, "  loaded %d frames @ %dHz\n", len(a.PCM), a.SampleRate)

	switch {
	case storeRate != 0:
		if !sample.InRange(storeRate) {
			return 0, fmt.Errorf("rate %d outside S950 range (%d..%d Hz)",
				storeRate, sample.MinSampleRateHz, sample.MaxSampleRateHz)
		}
		if storeRate != a.SampleRate {
			orig := a.SampleRate
			a = sample.ResampleTo(a, storeRate)
			fmt.Fprintf(os.Stderr, "  resampled %dHz -> %dHz (%d frames)\n",
				orig, a.SampleRate, len(a.PCM))
		}
	case !sample.InRange(a.SampleRate):
		orig := a.SampleRate
		a = sample.ResampleToS950Range(a, 0)
		fmt.Fprintf(os.Stderr, "  source %dHz out of range; resampled to %dHz (%d frames)\n",
			orig, a.SampleRate, len(a.PCM))
	}

	if o.MaxFrames > 0 && o.MaxFrames < len(a.PCM) {
		a.PCM = a.PCM[:o.MaxFrames]
		fmt.Fprintf(os.Stderr, "  truncated to %d frames\n", len(a.PCM))
	}

	words, err := sample.PCM16ToWords(a.PCM)
	if err != nil {
		return 0, fmt.Errorf("convert: %w", err)
	}

	finalSlot := byte(o.PreferredSlot)
	if picked, changed, perr := d.PickSlot(finalSlot); perr == nil {
		finalSlot = picked
		if changed {
			fmt.Fprintf(os.Stderr, "  slot %d occupied; using slot %d\n",
				o.PreferredSlot, finalSlot)
		}
	} else {
		fmt.Fprintf(os.Stderr, "  warning: catalog read failed (%v); using slot %d as-is\n",
			perr, o.PreferredSlot)
	}

	sent, drain, err := d.PutSampleOpenLoop(words, device.PutSampleOpts{
		Num:          finalSlot,
		SampleRateHz: a.SampleRate,
		LoopStart:    o.LoopStart,
		LoopEnd:      o.LoopEnd,
		Mode:         o.Mode,
	})
	if err != nil {
		return 0, fmt.Errorf("upload: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  queued %d bytes; waiting %s for MIDI drain...\n",
		sent, drain.Round(100*time.Millisecond))
	waitWithProgress(drain)

	if naks := d.CollectNAKs(500 * time.Millisecond); naks > 0 {
		return 0, fmt.Errorf("%d NAK(s) from S950 — sample may be truncated; please retry", naks)
	}

	// Patch the SPRM only when there's something explicit to set. We follow
	// the dxzl reference: leave SNOMP at the device's default (960 = C3) and
	// let the user adjust pitch via --tune or per-keygroup soft_tune in the
	// program JSON. The S950's actual rate↔pitch math has enough opaque
	// interacting variables that programmatic compensation isn't reliable.
	needsSPRM := o.Name != "" || o.TuneSemitones != 0 ||
		o.LoopStart != 0 || o.LoopEnd != 0
	if needsSPRM {
		time.Sleep(200 * time.Millisecond)
		p, err := d.GetParams(finalSlot)
		if err != nil {
			return 0, fmt.Errorf("read SPRM after upload: %w", err)
		}
		if o.Name != "" {
			p.Name = o.Name
		}
		if o.TuneSemitones != 0 {
			// --tune applies relative to the current SNOMP. Lower SNOMP =
			// higher playback at the same key, so positive tune SUBTRACTS.
			pitch := int32(p.NominalPitch) - int32(o.TuneSemitones*16)
			if pitch < 0 {
				pitch = 0
			}
			if pitch > 0xFFFF {
				pitch = 0xFFFF
			}
			p.NominalPitch = uint16(pitch)
		}
		if o.LoopStart != 0 || o.LoopEnd != 0 {
			p.Start = o.LoopStart
			p.End = o.LoopEnd
		}
		if err := d.SetParams(finalSlot, p); err != nil {
			return 0, fmt.Errorf("write SPRM: %w", err)
		}
	}

	return finalSlot, nil
}

func newProgramTemplateCmd() *cobra.Command {
	var name string
	var nKeygroups int
	var outPath string
	cmd := &cobra.Command{
		Use:   "program-template",
		Short: "Emit a starter program JSON you can edit then upload via put-program",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := protocol.NewDefaultProgram(name, nKeygroups)
			j := p.ToJSON()
			out := os.Stdout
			if outPath != "" {
				f, err := os.Create(outPath)
				if err != nil {
					return fmt.Errorf("create %s: %w", outPath, err)
				}
				defer f.Close()
				out = f
			}
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			if err := enc.Encode(&j); err != nil {
				return err
			}
			if outPath != "" {
				fmt.Fprintf(os.Stderr, "wrote %s (%d keygroup%s, defaults)\n",
					outPath, nKeygroups, plural(nKeygroups))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "NEW PROG", "program name (max 10 chars)")
	cmd.Flags().IntVar(&nKeygroups, "keygroups", 1,
		"number of keygroups to initialise (1..31)")
	cmd.Flags().StringVarP(&outPath, "output", "o", "",
		"write JSON to this file instead of stdout")
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
			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()
			start := time.Now()
			finalSlot, err := uploadSample(d, uploadSampleOpts{
				Path:          args[0],
				Rate:          rateStr,
				ChannelMode:   channelStr,
				TuneSemitones: tuneSemitones,
				MaxFrames:     maxFrames,
				LoopStart:     loopStart,
				LoopEnd:       loopEnd,
				Mode:          byte(mode),
				PreferredSlot: slot,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "done in %s (slot %d)\n",
				time.Since(start).Round(time.Millisecond), finalSlot)
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
