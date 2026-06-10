// CLI subcommands for reading + writing the S950's Overall Settings
// (OVS) and Drum Settings (DRS) blocks. OVS is fully modelled —
// `get-overall` pretty-prints every field, `set-overall` accepts a
// JSON file (matching the OverallSettings struct shape).  DRS is
// round-tripped as a blob: `get-drum` writes a binary file,
// `set-drum` reads it back. Useful as a backup/restore primitive
// even without per-field editing.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
)

func newGetOverallCmd() *cobra.Command {
	var asJSON bool
	var outPath string
	cmd := &cobra.Command{
		Use:   "get-overall",
		Short: "Read the device's Overall Settings (MIDI menu config)",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()
			// --output implies --json; redirect the writer to the file
			// and let the run core not care which sink it's writing to.
			w := cmd.OutOrStdout()
			if outPath != "" {
				f, err := os.Create(outPath)
				if err != nil {
					return fmt.Errorf("create %s: %w", outPath, err)
				}
				defer f.Close()
				w = f
				asJSON = true
			}
			return runGetOverall(w, d, asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	cmd.Flags().StringVarP(&outPath, "output", "o", "",
		"write JSON to this file instead of stdout (implies --json)")
	return cmd
}

// runGetOverall is newGetOverallCmd's testable core — reads the OVS
// block and renders to w either as the labelled table or as indented
// JSON. The cobra wrapper handles --output redirection (which simply
// swaps `w` for an opened file before calling here).
func runGetOverall(w io.Writer, d *device.Device, asJSON bool) error {
	o, err := d.GetOverall()
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(o)
	}
	printOverallTo(w, o)
	return nil
}

func newSetOverallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-overall <file.json>",
		Short: "Write Overall Settings from a JSON file produced by get-overall",
		Long: "Reads JSON in the shape get-overall emits (with --json) and writes the\n" +
			"settings back to the device. M1RS2 (controller-select) writes are\n" +
			"silently ignored by the device firmware — that flip can only be done\n" +
			"via the front-panel MIDI menu.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			buf, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read %s: %w", args[0], err)
			}
			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()
			return runSetOverall(d, buf)
		},
	}
	return cmd
}

// runSetOverall is newSetOverallCmd's testable core — unmarshals
// `jsonBytes` into an OverallSettings and writes it. Split from the
// file-read so tests can drive the decode + wire-send without touching
// the filesystem.
func runSetOverall(d *device.Device, jsonBytes []byte) error {
	var o protocol.OverallSettings
	if err := json.Unmarshal(jsonBytes, &o); err != nil {
		return fmt.Errorf("parse JSON: %w", err)
	}
	return d.SetOverall(&o)
}

func newGetDrumCmd() *cobra.Command {
	var outPath string
	cmd := &cobra.Command{
		Use:   "get-drum",
		Short: "Read Drum Settings as a raw 480-byte blob (backup / restore primitive)",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()
			return runGetDrum(cmd.ErrOrStderr(), d, outPath)
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "",
		"path to write the raw DRS bytes (omit for size-only report)")
	return cmd
}

// runGetDrum is newGetDrumCmd's testable core — fetches DRS, prints a
// status line to `status`, and writes the binary blob to `outPath`
// when non-empty. Status output is intentionally separate from the
// file write so the cobra wrapper can route them to stderr / disk
// while tests capture status only.
func runGetDrum(status io.Writer, d *device.Device, outPath string) error {
	drs, err := d.GetDrum()
	if err != nil {
		return err
	}
	if outPath == "" {
		fmt.Fprintf(status, "DRS: %d bytes (use -o file.bin to save)\n", len(drs.Bytes))
		return nil
	}
	if err := os.WriteFile(outPath, drs.Bytes, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Fprintf(status, "wrote %s (%d bytes)\n", outPath, len(drs.Bytes))
	return nil
}

func newSetDrumCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-drum <file.bin>",
		Short: "Write Drum Settings from a raw 480-byte file produced by get-drum -o",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			buf, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read %s: %w", args[0], err)
			}
			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()
			return runSetDrum(d, buf)
		},
	}
	return cmd
}

// runSetDrum is newSetDrumCmd's testable core — wraps the raw bytes
// in a DrumSettings and writes. Split from the file-read so tests can
// drive the wire-send path without touching the filesystem.
func runSetDrum(d *device.Device, drsBytes []byte) error {
	return d.SetDrum(&protocol.DrumSettings{Bytes: drsBytes})
}

// printOverallTo writes the human-readable OVS dump to w. Used by
// the `get-overall` cobra command's RunE (via runGetOverall).
func printOverallTo(w io.Writer, o *protocol.OverallSettings) {
	fmt.Fprintf(w, "Overall Settings\n")
	fmt.Fprintf(w, "  prog name     : %q\n", o.ProgName)
	fmt.Fprintf(w, "  basic ch      : %d %s\n", o.BasicChannel, omniLabel(o.OmniOn))
	fmt.Fprintf(w, "  midi tx ch    : %d\n", o.MidiTxChannel)
	fmt.Fprintf(w, "  ctrl mode     : %s (front-panel only — wire writes ignored)\n",
		controllerLabel(o.ControllerSelect))
	fmt.Fprintf(w, "  rs-232 baud   : %d\n", o.BaudRate)
	fmt.Fprintf(w, "  pitch wheel   : ±%d semitones\n", o.PitchWheelRange)
	fmt.Fprintf(w, "  loudness CC7  : %s\n", onOff(o.LoudnessOnCC7))
	fmt.Fprintf(w, "  MPEN (?)      : %s (purpose unconfirmed — see protocol/overall.go)\n", onOff(o.MPEN))
	fmt.Fprintf(w, "  rx-sim ch/k/v : %d / %d / %d\n", o.RxSimChannel, o.RxSimKey, o.RxSimVelocity)
}

func omniLabel(on bool) string {
	if on {
		return "(omni on)"
	}
	return ""
}
func controllerLabel(v uint8) string {
	switch v {
	case 1:
		return "MIDI"
	case 2:
		return "RS-232C"
	}
	return fmt.Sprintf("unknown (0x%02X)", v)
}
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
