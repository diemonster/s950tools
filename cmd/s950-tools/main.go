// Command s950-tools is a CLI for talking to an Akai S900/S950 sampler over MIDI.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
			return runCatalog(cmd.OutOrStdout(), d)
		},
	}
}

// runCatalog is newCatalogCmd's testable core — fetches the catalog
// and renders the table to w. Split from the cobra wiring so a fake
// transport can drive it without rtmidi. The cobra wrapper is now a
// thin 4-liner that opens a transport and hands off here.
func runCatalog(w io.Writer, d *device.Device) error {
	entries, err := d.Catalog()
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "%-4s %-4s %s\n", "TYPE", "NUM", "NAME")
	for _, e := range entries {
		typeName := "?"
		switch e.Type {
		case 'P':
			typeName = "PRG"
		case 'S':
			typeName = "SMP"
		}
		fmt.Fprintf(w, "%-4s %-4d %s\n", typeName, e.Num, e.Name)
	}
	return nil
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
			return runGetParams(cmd.OutOrStdout(), d, num, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	return cmd
}

// runGetParams is newGetParamsCmd's testable core — reads the SPRM
// for `num` and renders either the table or the JSON view to w.
// Split from the cobra wiring so the format choice can be regression-
// tested without rtmidi.
func runGetParams(w io.Writer, d *device.Device, num byte, asJSON bool) error {
	p, err := d.GetParams(num)
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(params2JSON(p))
	}
	printParamsTo(w, p, num)
	return nil
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

			if outPath == "" {
				return runGetProgram(cmd.OutOrStdout(), d, num)
			}
			// Buffer the fetch+encode and only touch the output file
			// once both succeeded — os.Create truncates, and a flaky
			// link mid-fetch must not zero out the user's previous
			// export at the same path.
			var buf bytes.Buffer
			if err := runGetProgram(&buf, d, num); err != nil {
				return err
			}
			if err := os.WriteFile(outPath, buf.Bytes(), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", outPath, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", outPath)
			return nil
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "",
		"write JSON to this file instead of stdout")
	return cmd
}

// runGetProgram is newGetProgramCmd's testable core — reads program
// `num` and JSON-encodes it to `w`. The cobra wrapper handles the
// --output redirect + the stderr "wrote N" status line; this core
// only cares about the wire fetch and the encode.
func runGetProgram(w io.Writer, d *device.Device, num byte) error {
	p, err := d.GetProgram(num)
	if err != nil {
		return err
	}
	j := p.ToJSON()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(&j)
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
			// Structural pre-validation BEFORE the transport opens —
			// FromJSON is what rejects corrupt _raw_header_hex /
			// keygroup hex, and a hand-edited kit file should fail
			// with that diagnostic, not a port error when no device
			// is attached. runPutProgram re-decodes internally; the
			// duplicate decode is microseconds against a wire write.
			if err := (&protocol.Program{}).FromJSON(&pj); err != nil {
				return fmt.Errorf("decode program: %w", err)
			}

			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()

			return runPutProgram(cmd.ErrOrStderr(), d, &pj, putProgramOpts{
				Slot:         byte(slot),
				SkipVerify:   skipVerify,
				ForceSamples: forceSamples,
				KitDir:       filepath.Dir(path),
			})
		},
	}
	cmd.Flags().IntVar(&slot, "slot", 0, "destination program slot (0..99)")
	cmd.Flags().BoolVar(&skipVerify, "no-verify", false,
		"skip the get-program verification read after writing")
	cmd.Flags().BoolVar(&forceSamples, "force-samples", false,
		"re-upload samples even when a sample of the same name already exists on the device")
	return cmd
}

// putProgramOpts is the flag bag for runPutProgram. Split off the
// cobra closure so tests can construct it directly.
type putProgramOpts struct {
	// Slot is the destination program slot (0..99).
	Slot byte
	// SkipVerify disables the post-write get-program verification
	// read — matches the --no-verify CLI flag. Tests usually set
	// this true to avoid having to synthesise a PRGM reply.
	SkipVerify bool
	// ForceSamples re-uploads samples even when a same-named entry
	// already exists in the device catalog. Default behaviour skips
	// the upload to save wire time during iterative kit builds.
	ForceSamples bool
	// KitDir is the directory used to resolve relative sample.File
	// paths against. The cobra wrapper passes filepath.Dir(jsonPath);
	// tests can use a tempdir.
	KitDir string
}

// runPutProgram is newPutProgramCmd's testable core. Given an
// already-decoded ProgramJSON, it orchestrates the manifest sample
// uploads (with skip-if-present), the program write, the NAK
// collection, and the optional verify read. Split from the cobra
// wrapper so the catalog-driven skip logic + the NAK-error path can
// be exercised with a fake transport.
func runPutProgram(status io.Writer, d *device.Device, pj *protocol.ProgramJSON, opts putProgramOpts) error {
	p := &protocol.Program{}
	if err := p.FromJSON(pj); err != nil {
		return fmt.Errorf("decode program: %w", err)
	}

	// --- Sample manifest, if any ---
	if len(pj.Samples) > 0 {
		existing := map[string]bool{}
		if cat, err := d.Catalog(); err == nil {
			for _, e := range cat {
				if e.Type == 'S' {
					existing[e.Name] = true
				}
			}
		} else {
			fmt.Fprintf(status, "warning: catalog read failed (%v); proceeding without skip-if-present\n", err)
		}

		for i, spec := range pj.Samples {
			name := resolveSampleName(spec)
			if existing[name] && !opts.ForceSamples {
				fmt.Fprintf(status, "[%d/%d] %s: already on device, skipping\n",
					i+1, len(pj.Samples), name)
				continue
			}
			filePath := spec.File
			if !filepath.IsAbs(filePath) {
				filePath = filepath.Join(opts.KitDir, filePath)
			}
			fmt.Fprintf(status, "[%d/%d] %s ← %s\n",
				i+1, len(pj.Samples), name, filePath)
			if _, err := uploadSample(status, d, uploadSampleOpts{
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
	if err := d.SetProgram(opts.Slot, p); err != nil {
		return fmt.Errorf("write program: %w", err)
	}
	naks := d.CollectNAKs(500 * time.Millisecond)
	if naks > 0 {
		return fmt.Errorf("write received %d NAK(s) — program may not have stored cleanly; please retry (and consider power-cycling the S950 if the target slot was already populated)", naks)
	}
	fmt.Fprintf(status, "wrote program %q (%d keygroup%s) to slot %d in %s\n",
		p.Name, len(p.Keygroups), plural(len(p.Keygroups)), opts.Slot,
		time.Since(start).Round(time.Millisecond))

	if !opts.SkipVerify {
		time.Sleep(200 * time.Millisecond)
		v, err := d.GetProgram(opts.Slot)
		if err != nil {
			return fmt.Errorf("verify (get-program): %w", err)
		}
		fmt.Fprintf(status, "verified: name=%q keygroups=%d\n",
			v.Name, len(v.Keygroups))
	}
	return nil
}

// resolveSampleName returns the SPRM name a sample manifest entry
// should be uploaded under: spec.Name verbatim when set, otherwise
// the file basename uppercased, extension stripped, truncated to the
// S950's 10-character limit. Pure helper — split from runPutProgram's
// loop so the name-derivation rules can be table-tested without a
// device.
func resolveSampleName(spec protocol.SampleSpec) string {
	if spec.Name != "" {
		return spec.Name
	}
	base := filepath.Base(spec.File)
	name := strings.ToUpper(strings.TrimSuffix(base, filepath.Ext(base)))
	if len(name) > 10 {
		name = name[:10]
	}
	return name
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
func uploadSample(status io.Writer, d *device.Device, o uploadSampleOpts) (byte, error) {
	a, words, err := prepareSampleAudio(status, o)
	if err != nil {
		return 0, err
	}

	finalSlot := byte(o.PreferredSlot)
	if picked, changed, perr := d.PickSlot(finalSlot); perr == nil {
		finalSlot = picked
		if changed {
			fmt.Fprintf(status, "  slot %d occupied; using slot %d\n",
				o.PreferredSlot, finalSlot)
		}
	} else {
		fmt.Fprintf(status, "  warning: catalog read failed (%v); using slot %d as-is\n",
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
	fmt.Fprintf(status, "  queued %d bytes; waiting %s for MIDI drain...\n",
		sent, drain.Round(100*time.Millisecond))
	waitWithProgressTo(status, drain, time.Second)

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
			p.NominalPitch = applyTuneToPitch(p.NominalPitch, o.TuneSemitones)
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

// prepareSampleAudio loads the source file, applies the requested
// rate / channel-mix transforms, truncates to MaxFrames, and converts
// to S950 12-bit words. Pure: no wire traffic, only filesystem + the
// internal/sample package. Split from uploadSample so the audio-shape
// path can be table-tested without a transport.
func prepareSampleAudio(status io.Writer, o uploadSampleOpts) (*sample.LoadedAudio, []uint16, error) {
	chMode, err := sample.ParseChannelMode(o.ChannelMode)
	if err != nil {
		return nil, nil, err
	}
	storeRate, err := sample.ParseRate(o.Rate)
	if err != nil {
		return nil, nil, err
	}
	a, err := sample.LoadAudio(o.Path, chMode)
	if err != nil {
		return nil, nil, fmt.Errorf("load %s: %w", o.Path, err)
	}
	fmt.Fprintf(status, "  loaded %d frames @ %dHz\n", len(a.PCM), a.SampleRate)

	switch {
	case storeRate != 0:
		if !sample.InRange(storeRate) {
			return nil, nil, fmt.Errorf("rate %d outside S950 range (%d..%d Hz)",
				storeRate, sample.MinSampleRateHz, sample.MaxSampleRateHz)
		}
		if storeRate != a.SampleRate {
			orig := a.SampleRate
			a = sample.ResampleTo(a, storeRate)
			fmt.Fprintf(status, "  resampled %dHz -> %dHz (%d frames)\n",
				orig, a.SampleRate, len(a.PCM))
		}
	case !sample.InRange(a.SampleRate):
		orig := a.SampleRate
		a = sample.ResampleToS950Range(a, 0)
		fmt.Fprintf(status, "  source %dHz out of range; resampled to %dHz (%d frames)\n",
			orig, a.SampleRate, len(a.PCM))
	}

	if o.MaxFrames > 0 && o.MaxFrames < len(a.PCM) {
		a.PCM = a.PCM[:o.MaxFrames]
		fmt.Fprintf(status, "  truncated to %d frames\n", len(a.PCM))
	}

	words, err := sample.PCM16ToWords(a.PCM)
	if err != nil {
		return nil, nil, fmt.Errorf("convert: %w", err)
	}
	return a, words, nil
}

// applyTuneToPitch returns the SNOMP value resulting from applying
// `semitones` worth of tune-by-ear adjustment to `current`. Positive
// semitones SUBTRACTS from SNOMP (lower SNOMP = higher playback at
// the same key — same direction dxzl/akai-s950 uses). Clamped to
// uint16 so a wild --tune value can't wrap around to a garbage pitch.
func applyTuneToPitch(current uint16, semitones float64) uint16 {
	// SNOMP resolution is 1/16 semitone: each semitone is 16 units.
	delta := int32(semitones * 16)
	pitch := int32(current) - delta
	if pitch < 0 {
		return 0
	}
	if pitch > 0xFFFF {
		return 0xFFFF
	}
	return uint16(pitch)
}

func newProgramTemplateCmd() *cobra.Command {
	var name string
	var nKeygroups int
	var outPath string
	cmd := &cobra.Command{
		Use:   "program-template",
		Short: "Emit a starter program JSON you can edit then upload via put-program",
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			if outPath != "" {
				f, err := os.Create(outPath)
				if err != nil {
					return fmt.Errorf("create %s: %w", outPath, err)
				}
				defer f.Close()
				w = f
			}
			if err := runProgramTemplate(w, name, nKeygroups); err != nil {
				return err
			}
			if outPath != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s (%d keygroup%s, defaults)\n",
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

// runProgramTemplate is newProgramTemplateCmd's testable core —
// builds a default Program with `nKeygroups` keygroups under `name`
// and indented-JSON encodes it to w. Pure: no wire, no filesystem.
// The cobra wrapper handles --output redirection by swapping w for an
// opened file before calling here.
func runProgramTemplate(w io.Writer, name string, nKeygroups int) error {
	p := protocol.NewDefaultProgram(name, nKeygroups)
	j := p.ToJSON()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(&j)
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
			finalSlot, err := uploadSample(cmd.ErrOrStderr(), d, uploadSampleOpts{
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
			fmt.Fprintf(cmd.ErrOrStderr(), "done in %s (slot %d)\n",
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

// waitWithProgressTo sleeps for `total`, writing a dot to `w` once
// per `tickEvery` so long transfers don't look hung. Short waits
// (total <= tickEvery) sleep silently. Used by uploadSample's MIDI-
// drain wait; the writer + tick seams make it test-pace-able.
func waitWithProgressTo(w io.Writer, total, tickEvery time.Duration) {
	if total <= tickEvery {
		time.Sleep(total)
		return
	}
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		<-tick.C
		fmt.Fprintf(w, ".")
	}
	fmt.Fprintln(w)
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

// printParamsTo writes the human-readable SPRM dump to w. Used by
// the `get-params` cobra command's RunE (via runGetParams).
func printParamsTo(w io.Writer, p *protocol.SampleParams, num byte) {
	fmt.Fprintf(w, "Sample %d: %q\n", num, p.Name)
	fmt.Fprintf(w, "  total words : %d\n", p.TotalWords)
	fmt.Fprintf(w, "  sample rate : %d Hz\n", p.SampleRateHz)
	fmt.Fprintf(w, "  nominal pch : %d (C3=960; 1/16-semitone)\n", p.NominalPitch)
	fmt.Fprintf(w, "  loud offset : %d\n", p.LoudOffset)
	fmt.Fprintf(w, "  replay mode : %q\n", p.ReplayMode)
	fmt.Fprintf(w, "  start       : %d\n", p.Start)
	fmt.Fprintf(w, "  end         : %d\n", p.End)
	fmt.Fprintf(w, "  loop length : %d\n", p.LoopLength)
	fmt.Fprintf(w, "  reversed    : %q\n", p.Reversed)
	if p.VelXFade != 0 {
		fmt.Fprintf(w, "  velocity xf : on\n")
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
