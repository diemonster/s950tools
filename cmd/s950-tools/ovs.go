// Shared OVS helpers for the CLI subcommands that read/write the
// S950's Overall Settings message.

package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/transport"
)

// requestOVS sends ROVS and returns the 80-byte payload from the
// device's OVS response. Skips any unrelated SysEx that arrives
// while waiting.
func requestOVS(t transport.Transport, channel byte, timeout time.Duration) ([]byte, error) {
	t.Drain()
	if err := t.Send(protocol.BuildAkaiRequest(channel, protocol.FuncROVS, 0)); err != nil {
		return nil, fmt.Errorf("send ROVS: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		msg, err := t.RecvSysEx(time.Until(deadline))
		if err != nil {
			return nil, fmt.Errorf("await OVS: %w", err)
		}
		akai, perr := protocol.ParseAkai(msg)
		if perr != nil {
			continue
		}
		if akai.Function == protocol.FuncOVS {
			return akai.Payload, nil
		}
	}
	return nil, errors.New("timed out waiting for OVS reply")
}
