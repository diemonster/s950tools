// CLI subcommand for `s950-tools set-baud` — writes the S950's
// RS-232 baud rate via OVS. Unlike M1RS2 (controller-select, which
// the firmware silently rejects), RSBAUD is honoured — dxzl's
// reference code uses the same path.
//
// Field layout: RSBAUD at envelope offset 79 (DW = 4 wire bytes),
// stored as baud/10. So 38400 → 3840, 76800 → 7680, 115200 → 11520.
// Range per dxzl's UI validation: 300..115200.
//
// After the write, the device switches its UART to the new rate
// immediately. The caller's next command must specify --baud N to
// match, or it'll see only garbage.

package main

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/sysex"
)

// rsbaudPayloadOffset is RSBAUD's envelope offset 79 minus the
// 7-byte AKAI header — gives the offset into AkaiMessage.Payload.
const rsbaudPayloadOffset = 79 - 7 // 72

func newSetBaudCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set-baud <rate>",
		Short: "Set the S950's RS-232 baud rate via OVS",
		Long:  "Writes the RSBAUD field of Overall Settings. The device switches its UART\nimmediately on receipt, so the next command must use --baud <rate> to match.\nSupported range: 300..115200 (typical: 9600, 19200, 38400, 57600, 76800, 115200).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rate, err := parseBaudRate(args[0])
			if err != nil {
				return err
			}
			d, cleanup, err := newDevice()
			if err != nil {
				return err
			}
			defer cleanup()
			return runSetBaud(cmd.ErrOrStderr(), d, rate)
		},
	}
	return cmd
}

// parseBaudRate parses the user-supplied baud arg and validates it
// against the S950's accepted range and multiple-of-10 constraint
// (RSBAUD stores rate/10, so non-multiples can't round-trip).
func parseBaudRate(arg string) (int, error) {
	var rate int
	if _, err := fmt.Sscanf(arg, "%d", &rate); err != nil {
		return 0, fmt.Errorf("parse rate: %w", err)
	}
	if rate < 300 || rate > 115200 {
		return 0, fmt.Errorf("rate %d outside supported range 300..115200", rate)
	}
	if rate%10 != 0 {
		return 0, fmt.Errorf("rate %d must be a multiple of 10 (RSBAUD stores rate/10)", rate)
	}
	return rate, nil
}

// runSetBaud is newSetBaudCmd's testable core — reads OVS, patches
// the RSBAUD field, writes OVS back, and emits status lines to
// `status`. Split from the cobra wiring so the OVS-patching math can
// be exercised with a fake transport.
func runSetBaud(status io.Writer, d *device.Device, rate int) error {
	payload, err := requestOVS(d.T, d.Channel, 4*time.Second)
	if err != nil {
		return fmt.Errorf("request OVS: %w", err)
	}
	if len(payload) < rsbaudPayloadOffset+4 {
		return fmt.Errorf("OVS payload too short: %d bytes (need at least %d)",
			len(payload), rsbaudPayloadOffset+4)
	}

	cur := sysex.DecodeDW([4]byte{
		payload[rsbaudPayloadOffset],
		payload[rsbaudPayloadOffset+1],
		payload[rsbaudPayloadOffset+2],
		payload[rsbaudPayloadOffset+3],
	})
	fmt.Fprintf(status,
		"current RSBAUD = %d (%d baud); changing to %d (%d baud)\n",
		cur, int(cur)*10, rate/10, rate)

	enc := sysex.EncodeDW(uint16(rate / 10))
	copy(payload[rsbaudPayloadOffset:rsbaudPayloadOffset+4], enc[:])

	env := protocol.BuildAkaiData(d.Channel, protocol.FuncOVS, 0, payload)
	if err := d.T.Send(env); err != nil {
		return fmt.Errorf("send OVS: %w", err)
	}
	// Give the device a moment to apply the change before the
	// transport is closed (close-during-mid-byte can strand the UART
	// in some FTDI drivers).
	time.Sleep(300 * time.Millisecond)
	fmt.Fprintf(status,
		"baud written. Reconnect with --baud %d (or %d if you're going back).\n",
		rate, int(cur)*10)
	return nil
}
