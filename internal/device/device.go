// Package device provides high-level operations against an Akai S900/S950 over
// MIDI: catalog listing, sample-parameter read, sample download, sample upload.
package device

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/sample"
	"github.com/bivers/s950/internal/transport"
)

// Device is a thin orchestrator on top of a Transport and a MIDI channel.
type Device struct {
	T       *transport.Transport
	Channel byte // S950 MIDI channel (0..15)

	// Timeouts.
	RequestTimeout time.Duration // simple request-response (catalog, params)
	DumpTimeout    time.Duration // full sample dump receive

	// AckPaceInterval controls how often pre-emptive ACKs are sent while
	// receiving a sample dump (see GetSample). 30ms keeps comfortably ahead of
	// the S950's per-block ~40ms transmit time.
	AckPaceInterval time.Duration
}

// New constructs a Device with sensible defaults.
//
// Hardware note: 50 ms ACK pacing is empirically the sweet spot on a
// USB-MIDI router. Faster than that (30 ms) saturates the bus and we see
// random byte drops mid-dump; slower (100 ms+) also drops bytes because the
// S950's per-block timing gets ragged. 50 ms tracks block transmit time (a
// 122-byte block is ~39 ms on a 31250-baud MIDI wire) with enough margin.
func New(t *transport.Transport, channel byte) *Device {
	return &Device{
		T:               t,
		Channel:         channel,
		RequestTimeout:  3 * time.Second,
		DumpTimeout:     5 * time.Minute,
		AckPaceInterval: 50 * time.Millisecond,
	}
}

// Catalog requests the device's program and sample list.
func (d *Device) Catalog() ([]protocol.CatalogEntry, error) {
	d.T.Drain()
	if err := d.T.Send(protocol.BuildAkaiRequest(d.Channel, protocol.FuncRCAT, 0)); err != nil {
		return nil, fmt.Errorf("send RCAT: %w", err)
	}
	reply, err := d.waitForFunction(protocol.FuncCAT, d.RequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("await CAT: %w", err)
	}
	return protocol.ParseCatalog(reply.Payload)
}

// GetParams reads the 120-byte sample-parameter block for sample N.
func (d *Device) GetParams(num byte) (*protocol.SampleParams, error) {
	d.T.Drain()
	if err := d.T.Send(protocol.BuildAkaiRequest(d.Channel, protocol.FuncRSPRM, num)); err != nil {
		return nil, fmt.Errorf("send RSPRM: %w", err)
	}
	reply, err := d.waitForFunction(protocol.FuncSPRM, d.RequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("await SPRM: %w", err)
	}
	if len(reply.Payload) != 120 {
		return nil, fmt.Errorf("SPRM payload is %d bytes, want 120", len(reply.Payload))
	}
	return protocol.ParseSampleParams(reply.Payload)
}

// waitForFunction drains incoming SysEx until one decodes as an AKAI message
// with the expected function code, or the timeout fires.
func (d *Device) waitForFunction(fn byte, timeout time.Duration) (*protocol.AkaiMessage, error) {
	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, errors.New("timeout")
		}
		raw, err := d.T.RecvSysEx(left)
		if err != nil {
			return nil, err
		}
		// Skip non-AKAI messages (stray handshakes etc).
		msg, perr := protocol.ParseAkai(raw)
		if perr != nil {
			continue
		}
		if msg.Function != fn {
			continue
		}
		return msg, nil
	}
}

// SampleDump is a decoded sample dump.
type SampleDump struct {
	Header protocol.SampleDumpHeader
	Words  []uint16 // length == Header.TotalWords
}

// GetSampleOpts controls the closed-loop ACK strategy on download.
type GetSampleOpts struct {
	// NoAckPump disables the pre-emptive ACK pump. Only useful for testing
	// whether the S950 will dump in open-loop mode (it normally requires
	// per-block ACKs).
	NoAckPump bool
	// SingleAck sends just one ACK after the RSD (no per-block pump).
	SingleAck bool
}

// GetSample downloads sample number num from the device. If rawSink is
// non-nil it receives the unprocessed SysEx bytes (useful for debugging).
//
// Closed-loop strategy: the S950 sends one big SysEx (F0 7E 01 <header>
// <block 0> ... <block N> F7) and pauses between blocks waiting for an ACK.
// Our MIDI driver only surfaces the full SysEx after F7, so we cannot react
// per-block. Instead, we stream pre-emptive ACKs at AckPaceInterval starting
// immediately after the RSD request — keeping an ACK perpetually available in
// the S950's input buffer.
func (d *Device) GetSample(ctx context.Context, num byte, opts GetSampleOpts, rawSink func([]byte)) (*SampleDump, error) {
	d.T.Drain()

	// Kick off the dump.
	if err := d.T.Send(protocol.BuildRequestSampleDump(num)); err != nil {
		return nil, fmt.Errorf("send RSD: %w", err)
	}

	// Start ACK pumping in the background, unless disabled.
	pumpCtx, cancelPump := context.WithCancel(ctx)
	defer cancelPump()
	pumpDone := make(chan struct{})
	switch {
	case opts.NoAckPump:
		close(pumpDone)
	case opts.SingleAck:
		_ = d.T.Send(protocol.BuildHandshake(protocol.CodeACKS))
		close(pumpDone)
	default:
		go d.pumpAcks(pumpCtx, pumpDone)
	}

	// Wait for the dump.
	deadline := time.Now().Add(d.DumpTimeout)
	var raw []byte
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, fmt.Errorf("timeout waiting for sample dump (%v)", d.DumpTimeout)
		}
		m, err := d.T.RecvSysEx(left)
		if err != nil {
			return nil, err
		}
		// Skip stray handshakes (the S950 may echo something) and any other
		// non-dump traffic.
		if len(m) < 19 || m[1] != protocol.UniversalNRT || m[2] != protocol.CodeSD {
			continue
		}
		raw = m
		break
	}
	cancelPump()
	<-pumpDone

	if rawSink != nil {
		rawSink(raw)
	}
	return decodeDump(raw)
}

// pumpAcks emits an ACK every d.AckPaceInterval until ctx is cancelled.
func (d *Device) pumpAcks(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ack := protocol.BuildHandshake(protocol.CodeACKS)
	// Fire one immediately so the header has an ACK waiting.
	_ = d.T.Send(ack)
	t := time.NewTicker(d.AckPaceInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.T.Send(ack); err != nil {
				return
			}
		}
	}
}

// decodeDump parses a complete dump SysEx (header + blocks + F7) into a Dump.
//
// Note: contrary to the spec doc, the S950 sends one block more than
// ceil(total_words / 60) — the doc flagged the block count as
// [VERIFY ON HARDWARE]. We accept either count and trim padding via
// TotalWords from the header.
func decodeDump(buf []byte) (*SampleDump, error) {
	if len(buf) < 19+1 || buf[len(buf)-1] != protocol.EOX {
		return nil, fmt.Errorf("malformed sample dump: %d bytes", len(buf))
	}
	hdr, err := protocol.ParseHeader(buf[:19])
	if err != nil {
		return nil, err
	}
	body := buf[19 : len(buf)-1] // strip header (incl. F0 7E 01) and trailing F7
	if len(body)%protocol.BlockSize != 0 {
		return nil, fmt.Errorf("dump body is %d bytes, not a multiple of block size %d",
			len(body), protocol.BlockSize)
	}
	nBlocks := len(body) / protocol.BlockSize
	expectedMin := protocol.NumBlocks(hdr.TotalWords)
	if nBlocks < expectedMin {
		return nil, fmt.Errorf("dump body has %d blocks, expected at least %d for %d words",
			nBlocks, expectedMin, hdr.TotalWords)
	}
	words := make([]uint16, 0, hdr.TotalWords)
	remaining := int(hdr.TotalWords)
	for i := 0; i < nBlocks && remaining > 0; i++ {
		_, w, err := protocol.DecodeBlock(body[i*protocol.BlockSize : (i+1)*protocol.BlockSize])
		if err != nil {
			return nil, fmt.Errorf("block %d: %w", i, err)
		}
		take := protocol.WordsPerBlock
		if take > remaining {
			take = remaining
		}
		for j := 0; j < take; j++ {
			words = append(words, w[j])
		}
		remaining -= take
	}
	return &SampleDump{Header: *hdr, Words: words}, nil
}

// PutSampleOpts controls a sample upload.
type PutSampleOpts struct {
	// Num is the S950 slot to write to (0..99).
	Num byte
	// SampleRateHz is the source rate in Hz; converted to the S950's ns period.
	SampleRateHz uint32
	// LoopStart / LoopEnd: when LoopEnd-LoopStart < 5, treated as one-shot
	// (and the S950 fills loop-start to total-5 to signal non-looping).
	LoopStart uint32
	LoopEnd   uint32
	// Mode is 0 (looping) or 1 (alternating).
	Mode byte
}

// MIDI byte rate at 31250 baud (10 bits / byte framing).
const midiBytesPerSecond = 3125

// ExpectedDrainTime estimates how long it takes the MIDI output to actually
// transmit n bytes at MIDI's native baud rate. Used to wait after a put-sample
// since rtmidi/CoreMIDI buffer asynchronously and Send returns immediately.
func ExpectedDrainTime(n int) time.Duration {
	// + 10% safety margin and a fixed 200ms floor.
	d := time.Duration(n) * time.Second / midiBytesPerSecond
	d = d * 11 / 10
	if d < 200*time.Millisecond {
		d = 200 * time.Millisecond
	}
	return d
}

// PutSampleOpenLoop uploads a sample to the device in open-loop mode: the
// whole dump goes out as a single big SysEx without waiting for per-block
// ACKs. The S950 doc allows this and recommends it for v1 uploads.
//
// Returns the number of MIDI bytes queued and the wall-clock time the OS will
// actually take to transmit them at MIDI's 31250-baud line rate. The caller is
// responsible for waiting at least that long before issuing the next request,
// since rtmidi's Send returns as soon as the bytes are queued into the OS.
func (d *Device) PutSampleOpenLoop(words []uint16, opts PutSampleOpts) (sent int, drain time.Duration, err error) {
	if uint32(len(words)) < sample.MinTotalWords {
		return 0, 0, sample.ErrLengthTooShort
	}
	if uint32(len(words)) > sample.MaxTotalWords {
		return 0, 0, sample.ErrLengthTooLong
	}
	period, perr := sample.HzToPeriodNS(opts.SampleRateHz)
	if perr != nil {
		return 0, 0, perr
	}

	loopStart, loopEnd := opts.LoopStart, opts.LoopEnd
	// One-shot encoding per the doc: loop start >= total_words - 5.
	if loopEnd <= loopStart+5 {
		if uint32(len(words)) > 5 {
			loopStart = uint32(len(words)) - 5
			loopEnd = uint32(len(words)) - 1
		}
	}

	header := protocol.SampleDumpHeader{
		Num:         uint16(opts.Num),
		BitsPerWord: 12,
		PeriodNS:    period,
		TotalWords:  uint32(len(words)),
		LoopStart:   loopStart,
		LoopEnd:     loopEnd,
		Mode:        opts.Mode,
	}

	// Pre-size buffer: header + blocks + trailing F7.
	nBlocks := protocol.NumBlocks(uint32(len(words)))
	buf := make([]byte, 0, 19+nBlocks*protocol.BlockSize+1)
	buf = append(buf, header.EncodeHeader()...)

	for i := 0; i < nBlocks; i++ {
		var blk [protocol.WordsPerBlock]uint16
		for k := 0; k < protocol.WordsPerBlock; k++ {
			idx := i*protocol.WordsPerBlock + k
			if idx < len(words) {
				blk[k] = words[idx]
			} else {
				blk[k] = sample.SilenceWord // pad with offset-binary silence
			}
		}
		buf = append(buf, protocol.EncodeBlock(i, blk)...)
	}
	buf = append(buf, protocol.EOX)

	if err := d.T.Send(buf); err != nil {
		return 0, 0, err
	}
	return len(buf), ExpectedDrainTime(len(buf)), nil
}
