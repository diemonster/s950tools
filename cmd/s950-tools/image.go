// CLI subcommand for inspecting Akai S900/S950 floppy images (the
// raw 800 KB .img files a Gotek/FlashFloppy mounts). Host-side only
// — no device connection needed. The first consumer is validating
// Translator-built images against our format understanding before
// the GUI export feature trusts it; it doubles as a quick "what's
// on this disk" tool.

package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/bivers/s950/internal/akaidisk"
)

func newImageInspectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image-inspect <disk.img>",
		Short: "List the contents of an S900/S950 floppy image (Gotek/FlashFloppy .img)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read %s: %w", args[0], err)
			}
			im, err := akaidisk.Parse(data)
			if err != nil {
				return err
			}
			return runImageInspect(cmd.OutOrStdout(), im)
		},
	}
	return cmd
}

// runImageInspect prints the volume directory plus decoded details
// for the file types we model. Testable core — takes the parsed
// image and a writer.
func runImageInspect(w io.Writer, im *akaidisk.Image) error {
	entries := im.Entries()
	fmt.Fprintf(w, "%-12s %-4s %8s  %s\n", "NAME", "TYPE", "BYTES", "DETAIL")
	for _, e := range entries {
		detail := ""
		switch e.Type {
		case akaidisk.TypeSample:
			detail = sampleDetail(im, e)
		case akaidisk.TypeProgram:
			detail = programDetail(im, e)
		case akaidisk.TypeOverall:
			detail = overallDetail(im, e)
		}
		fmt.Fprintf(w, "%-12s %-4c %8d  %s\n", e.Name, e.Type, e.Size, detail)
	}
	fmt.Fprintf(w, "%d file(s), %d free blocks (%d KB)\n",
		len(entries), im.FreeBlocks(), im.FreeBlocks())
	return nil
}

func sampleDetail(im *akaidisk.Image, e akaidisk.Entry) string {
	f, err := im.ReadFile(e)
	if err != nil || len(f) < 60 {
		return fmt.Sprintf("(unreadable: %v)", err)
	}
	slen := int(f[16]) | int(f[17])<<8 | int(f[18])<<16 | int(f[19])<<24
	rate := int(f[20]) | int(f[21])<<8
	return fmt.Sprintf("%d words @ %d Hz, mode %c", slen, rate, f[26])
}

func programDetail(im *akaidisk.Image, e akaidisk.Entry) string {
	f, err := im.ReadFile(e)
	if err != nil || len(f) < 38 {
		return fmt.Sprintf("(unreadable: %v)", err)
	}
	return fmt.Sprintf("%d keygroup(s)", f[23])
}

func overallDetail(im *akaidisk.Image, e akaidisk.Entry) string {
	f, err := im.ReadFile(e)
	if err != nil || len(f) < 40 {
		return fmt.Sprintf("(unreadable: %v)", err)
	}
	port := "MIDI"
	if f[29] == 2 {
		port = "RS-232C"
	}
	baud := (int(f[36]) | int(f[37])<<8) * 10
	return fmt.Sprintf("control by %s, baud %d", port, baud)
}
