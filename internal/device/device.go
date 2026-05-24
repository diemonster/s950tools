// Package device provides high-level operations against an Akai S900/S950 over
// MIDI: catalog listing, sample-parameter read, sample upload.
package device

import (
	"errors"
	"fmt"
	"time"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/sample"
	"github.com/bivers/s950/internal/transport"
)

// Device is a thin orchestrator on top of a Transport and a MIDI channel.
// All high-level S950 operations (Catalog, GetProgram, SetParams,
// PutSampleOpenLoop, ...) hang off this type.
type Device struct {
	// T is the underlying MIDI transport. The Device does not own it; the
	// caller is responsible for opening and closing the transport.
	T *transport.Transport
	// Channel is the S950's MIDI channel (0..15) — the low 4 bits of byte 2
	// in every AKAI-exclusive message we send.
	Channel byte
	// RequestTimeout bounds simple request-response cycles (Catalog,
	// GetParams). Long-running flows (GetProgram, sample dumps) use their own
	// longer deadlines internally.
	RequestTimeout time.Duration
}

// PutSampleOpts controls a single sample upload via PutSampleOpenLoop.
type PutSampleOpts struct {
	// Num is the S950 slot to write to (0..99).
	Num byte
	// SampleRateHz is the source rate in Hz; converted to the S950's ns
	// period via sample.HzToPeriodNS.
	SampleRateHz uint32
	// LoopStart is the first word of the loop, in words. When LoopEnd-LoopStart
	// is less than 5, the upload is treated as one-shot and the S950's
	// "loop_start >= total-5" non-looping sentinel is encoded automatically.
	LoopStart uint32
	// LoopEnd is the last word of the loop, in words. See LoopStart for the
	// one-shot fallback behaviour.
	LoopEnd uint32
	// Mode selects loop behaviour: 0 = looping, 1 = alternating.
	Mode byte
}

// MIDI byte rate at 31250 baud (10 bits / byte framing).
const midiBytesPerSecond = 3125

// New constructs a Device with sensible defaults.
func New(t *transport.Transport, channel byte) *Device {
	return &Device{
		T:              t,
		Channel:        channel,
		RequestTimeout: 3 * time.Second,
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

// GetProgram requests program N from the device and parses the PRGM payload
// into a Program (header + keygroups).
func (d *Device) GetProgram(num byte) (*protocol.Program, error) {
	d.T.Drain()
	if err := d.T.Send(protocol.BuildAkaiRequest(d.Channel, protocol.FuncRPRGM, num)); err != nil {
		return nil, fmt.Errorf("send RPRGM: %w", err)
	}
	// Programs can be up to 31 keygroups → 76+31*140 = 4416 payload bytes →
	// roughly 4.5 KB on the wire. Allow longer for the receive.
	reply, err := d.waitForFunction(protocol.FuncPRGM, 8*time.Second)
	if err != nil {
		return nil, fmt.Errorf("await PRGM: %w", err)
	}
	return protocol.ParseProgram(reply.Payload)
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

// SetProgram writes a Program (header + keygroups) to the device at slot
// num. The encoded payload size scales with keygroup count: 76 + N*140 bytes,
// from ~216 bytes (1 keygroup) up to ~4416 bytes (31 keygroups). At MIDI's
// 31250 baud that's ~70ms to ~1.4s of wire time.
//
// As with SetParams, the S950 does not reply to PRGM writes — caller should
// verify via GetProgram if it cares.
func (d *Device) SetProgram(num byte, p *protocol.Program) error {
	payload := p.EncodePayload()
	msg := protocol.BuildAkaiData(d.Channel, protocol.FuncPRGM, num, payload)
	if err := d.T.Send(msg); err != nil {
		return fmt.Errorf("send PRGM: %w", err)
	}
	time.Sleep(ExpectedDrainTime(len(msg)))
	return nil
}

// SetParams writes a 120-byte SPRM payload back to the device. Use this
// after a sample dump to override the S950's auto-computed SSTART/SEND/SLOOP
// defaults (which it sometimes leaves stale when overwriting a slot), or to
// adjust SNOMP for retuning. num must match the slot you're targeting; the
// SPRM's encoded sample number doesn't matter to the S950 — the message-
// header num does.
func (d *Device) SetParams(num byte, p *protocol.SampleParams) error {
	payload := p.EncodePayload()
	msg := protocol.BuildAkaiData(d.Channel, protocol.FuncSPRM, num, payload)
	if err := d.T.Send(msg); err != nil {
		return fmt.Errorf("send SPRM: %w", err)
	}
	// The S950 doesn't reply to SPRM writes, but the bytes still take wall-
	// clock time to drain at MIDI rate (129 bytes ~= 41 ms).
	time.Sleep(ExpectedDrainTime(len(msg)))
	return nil
}

// PickSlot returns preferred when that slot is unoccupied; otherwise the
// lowest empty slot in [0, 99]. The S950's boot-time "TONE" placeholder is
// treated as empty since it's safe to overwrite — only user-stored samples
// cause the overwrite NAK storm.
//
// Returns 0 with an error if the catalog can't be read or all 100 slots
// are occupied.
func (d *Device) PickSlot(preferred byte) (byte, bool, error) {
	cat, err := d.Catalog()
	if err != nil {
		return 0, false, fmt.Errorf("read catalog: %w", err)
	}
	occupied := map[byte]bool{}
	for _, e := range cat {
		if e.Type == 'S' && e.Name != "TONE" {
			occupied[e.Num] = true
		}
	}
	if !occupied[preferred] {
		return preferred, false, nil
	}
	for i := 0; i < 100; i++ {
		if !occupied[byte(i)] {
			return byte(i), true, nil
		}
	}
	return 0, false, errors.New("all 100 sample slots are occupied")
}

// CollectNAKs drains the inbound SysEx queue for `window` time and counts any
// system-exclusive-common NAK messages (F0 7E 7E F7) the S950 emitted.
// Used after an open-loop sample dump to detect that the device wasn't able
// to receive the audio cleanly — any NAK means the stored sample may be
// truncated and the upload should be retried.
func (d *Device) CollectNAKs(window time.Duration) int {
	deadline := time.Now().Add(window)
	n := 0
	for time.Now().Before(deadline) {
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		msg, err := d.T.RecvSysEx(left)
		if err != nil {
			break
		}
		if ok, code := protocol.IsHandshake(msg); ok && code == protocol.CodeNAKS {
			n++
		}
	}
	return n
}

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
