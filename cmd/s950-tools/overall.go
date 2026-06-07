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
	"os"

	"github.com/spf13/cobra"

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
			o, err := d.GetOverall()
			if err != nil {
				return err
			}
			if asJSON || outPath != "" {
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
				return enc.Encode(o)
			}
			printOverall(o)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON instead of a table")
	cmd.Flags().StringVarP(&outPath, "output", "o", "",
		"write JSON to this file instead of stdout (implies --json)")
	return cmd
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
			var o protocol.OverallSettings
			if err := json.Unmarshal(buf, &o); err != nil {
				return fmt.Errorf("parse JSON: %w", err)
			}
			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()
			return d.SetOverall(&o)
		},
	}
	return cmd
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
			drs, err := d.GetDrum()
			if err != nil {
				return err
			}
			if outPath == "" {
				fmt.Fprintf(os.Stderr, "DRS: %d bytes (use -o file.bin to save)\n", len(drs.Bytes))
				return nil
			}
			if err := os.WriteFile(outPath, drs.Bytes, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", outPath, err)
			}
			fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", outPath, len(drs.Bytes))
			return nil
		},
	}
	cmd.Flags().StringVarP(&outPath, "output", "o", "",
		"path to write the raw DRS bytes (omit for size-only report)")
	return cmd
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
			return d.SetDrum(&protocol.DrumSettings{Bytes: buf})
		},
	}
	return cmd
}

func printOverall(o *protocol.OverallSettings) {
	fmt.Printf("Overall Settings\n")
	fmt.Printf("  prog name     : %q\n", o.ProgName)
	fmt.Printf("  basic ch      : %d %s\n", o.BasicChannel, omniLabel(o.OmniOn))
	fmt.Printf("  midi tx ch    : %d\n", o.MidiTxChannel)
	fmt.Printf("  ctrl mode     : %s (front-panel only — wire writes ignored)\n",
		controllerLabel(o.ControllerSelect))
	fmt.Printf("  rs-232 baud   : %d\n", o.BaudRate)
	fmt.Printf("  pitch wheel   : ±%d semitones\n", o.PitchWheelRange)
	fmt.Printf("  loudness CC7  : %s\n", onOff(o.LoudnessOnCC7))
	fmt.Printf("  MPEN (?)      : %s (purpose unconfirmed — see protocol/overall.go)\n", onOff(o.MPEN))
	fmt.Printf("  rx-sim ch/k/v : %d / %d / %d\n", o.RxSimChannel, o.RxSimKey, o.RxSimVelocity)
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
