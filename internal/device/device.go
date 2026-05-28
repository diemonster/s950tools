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
	// T is the underlying transport — MIDI by default, but the
	// interface is backend-agnostic so a future RS232 implementation
	// can drop in without touching any of the protocol code that
	// reads/writes through it. The Device does not own the transport;
	// the caller is responsible for opening and closing it.
	T transport.Transport
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
func New(t transport.Transport, channel byte) *Device {
	return &Device{
		T:              t,
		Channel:        channel,
		RequestTimeout: 3 * time.Second,
	}
}

// Ping sends a small request (RCAT — catalog) and waits for any
// reply within timeout. Used as a pre-flight before long-running
// operations (sample upload) where a silently-unresponsive device
// would otherwise show false success: bytes go out, no NAKs come
// back (no NAK = no listener, but our code can't tell the
// difference from "all good"), the dialog reports success and the
// upload never landed.
//
// We don't validate the reply contents — any inbound SysEx counts
// as "device is alive on the wire." A response means the bytes
// can flow both directions; absence means the device isn't seeing
// our traffic (wrong controller-select mode, cable issue, etc.).
func (d *Device) Ping(timeout time.Duration) error {
	d.T.Drain()
	if err := d.T.Send(protocol.BuildAkaiRequest(d.Channel, protocol.FuncRCAT, 0)); err != nil {
		return fmt.Errorf("send ping: %w", err)
	}
	if _, err := d.T.RecvSysEx(timeout); err != nil {
		return fmt.Errorf("no reply within %v: %w", timeout, err)
	}
	return nil
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
//
// Device-side cache caveat: some SPRM fields (notably the Reversed
// flag) live in an "active edit buffer" the S950 only refreshes when
// the sample is re-selected on the front panel (ENT keypress). SysEx
// writes update the stored slot but not the active buffer, so a
// freshly-toggled Reverse won't take effect on the next playback
// until the user presses ENT on the device. We tried a follow-up
// RSPRM read to force a refresh — empirically it didn't help — and
// removed it. This is now a documented hardware limitation; the
// GUI surfaces a tooltip on the Reverse toggle.
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

// GetOverall reads the device's Overall Settings (OVS) block — the
// MIDI-menu device-wide config (default program name, channels,
// pitch wheel range, RS-232 baud, etc.).
func (d *Device) GetOverall() (*protocol.OverallSettings, error) {
	d.T.Drain()
	if err := d.T.Send(protocol.BuildAkaiRequest(d.Channel, protocol.FuncROVS, 0)); err != nil {
		return nil, fmt.Errorf("send ROVS: %w", err)
	}
	reply, err := d.waitForFunction(protocol.FuncOVS, d.RequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("await OVS: %w", err)
	}
	return protocol.ParseOverallSettings(reply.Payload)
}

// SetOverall writes Overall Settings back. The S950 firmware
// silently ignores writes to the M1RS2 (controller-select) field
// — that mode flip can only be done via the front-panel MIDI menu.
// Other fields (baud, channels, etc.) take effect immediately;
// if BaudRate changed the caller is responsible for re-opening the
// transport at the new rate.
func (d *Device) SetOverall(o *protocol.OverallSettings) error {
	payload, err := o.EncodePayload()
	if err != nil {
		return fmt.Errorf("encode OVS: %w", err)
	}
	msg := protocol.BuildAkaiData(d.Channel, protocol.FuncOVS, 0, payload)
	if err := d.T.Send(msg); err != nil {
		return fmt.Errorf("send OVS: %w", err)
	}
	time.Sleep(ExpectedDrainTime(len(msg)))
	return nil
}

// GetDrum reads the device's Drum Settings (DRS) — 8 hardware
// drum-input definitions, 480 data bytes total. Currently opaque:
// the byte layout isn't part of our protocol model so the returned
// struct only carries the raw bytes, suitable for backup +
// round-trip restore via SetDrum.
func (d *Device) GetDrum() (*protocol.DrumSettings, error) {
	d.T.Drain()
	if err := d.T.Send(protocol.BuildAkaiRequest(d.Channel, protocol.FuncRDRS, 0)); err != nil {
		return nil, fmt.Errorf("send RDRS: %w", err)
	}
	reply, err := d.waitForFunction(protocol.FuncDRS, d.RequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("await DRS: %w", err)
	}
	return protocol.ParseDrumSettings(reply.Payload)
}

// SetDrum writes a DRS payload back to the device. Pairs with
// GetDrum for backup/restore — round-trip is byte-identical.
func (d *Device) SetDrum(s *protocol.DrumSettings) error {
	payload, err := s.EncodePayload()
	if err != nil {
		return fmt.Errorf("encode DRS: %w", err)
	}
	msg := protocol.BuildAkaiData(d.Channel, protocol.FuncDRS, 0, payload)
	if err := d.T.Send(msg); err != nil {
		return fmt.Errorf("send DRS: %w", err)
	}
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

// GetSampleAudio requests the audio for sample `num` from the S950 (Phase 1C
// — user-initiated "Copy from S950"). Sends a Request-Sample-Dump (RSD) and
// reads back the resulting one-big-SysEx dump, parsing it into a flat 12-bit
// words buffer via ParseSampleDumpEnvelope.
//
// **Pre-emptive ACK pump strategy** (verified on hardware):
//
//   The S950 sends the entire dump as ONE long SysEx envelope (F0 7E 01
//   <header bytes> <block 0> <block 1> ... <block N> F7), but it pauses
//   between blocks waiting for our ACK before resuming. rtmidi only
//   surfaces complete F0..F7 envelopes, so we cannot react per-block
//   inside that single envelope. Instead, we start a background goroutine
//   that fires a 4-byte ACK (`F0 7E 7F F7`) at AckPaceInterval starting
//   immediately after the RSD — keeping an ACK perpetually available in
//   the S950's input buffer so it never stalls. The pump shuts down once
//   the final envelope lands.
//
//   Per-block synchronous ACKing CAN'T work with rtmidi because the
//   header-and-blocks come as a single unbroken envelope; we have nothing
//   to react to between them. The pump approach is the correct shape.
//
// Returns the parsed header + words12 trimmed to header.TotalWords.
// Bounded by timeout (size generously: ~7min at MIDI baud for max-size
// dumps plus safety margin).
func (d *Device) GetSampleAudio(num byte, timeout time.Duration) (*protocol.SampleDumpHeader, []uint16, error) {
	// Dispatch on the transport's capabilities: if it surfaces raw
	// inbound bytes (RS-232), use the per-block synchronous ACK path
	// — closes the loop tightly and removes the pre-emptive pump's
	// idle gap between blocks. MIDI rtmidi only delivers post-F7
	// envelopes so we fall back to the pump strategy that's been
	// hardware-verified for short samples.
	if pt, ok := d.T.(transport.PartialRecvTransport); ok {
		return d.getSampleAudioStreaming(pt, num, timeout)
	}
	return d.getSampleAudioPump(num, timeout)
}

// getSampleAudioPump is the MIDI-compatible receive path: send RSD,
// fire a pre-emptive ACK pump, wait for one big F0..F7 envelope, then
// parse. See the GetSampleAudio doc-comment for why this exists.
func (d *Device) getSampleAudioPump(num byte, timeout time.Duration) (*protocol.SampleDumpHeader, []uint16, error) {
	d.T.Drain()
	if err := d.T.Send(protocol.BuildRequestSampleDump(num)); err != nil {
		return nil, nil, fmt.Errorf("send RSD: %w", err)
	}

	// Start the ACK pump IMMEDIATELY — matches the original
	// 7cdf2b5 implementation that was demonstrated working on
	// hardware. The first ACK fires synchronously inside the
	// goroutine before the ticker starts, so by the time the
	// device finishes parsing our RSD an ACK is already in its
	// input buffer ready to release the first block.
	pumpStop := make(chan struct{})
	pumpDone := make(chan struct{})
	go d.pumpAcks(pumpStop, pumpDone)
	defer func() {
		close(pumpStop)
		<-pumpDone
	}()

	deadline := time.Now().Add(timeout)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return nil, nil, errors.New("timeout waiting for sample dump")
		}
		msg, err := d.T.RecvSysEx(left)
		if err != nil {
			return nil, nil, fmt.Errorf("recv sample dump: %w", err)
		}
		if ok, code := protocol.IsHandshake(msg); ok {
			if code == protocol.CodeNAKS {
				return nil, nil, errors.New("device NAK'd sample dump request")
			}
			if code == protocol.CodeASD {
				return nil, nil, errors.New("device aborted sample dump")
			}
			continue
		}
		// Skip non-dump SysEx (catalog responses, etc.)
		if len(msg) < 19 || msg[1] != protocol.UniversalNRT || msg[2] != protocol.CodeSD {
			continue
		}
		// Skip header-only envelopes (the device sometimes flushes
		// the header as its own envelope mid-pump; we want the
		// envelope that carries the data blocks).
		if len(msg) == headerEnvelopeBytes {
			continue
		}
		return ParseSampleDumpEnvelope(msg, num)
	}
}

// getSampleAudioStreaming is the RS-232-capable receive path. We see
// the inbound byte stream as it arrives (vs MIDI's atomic-envelope
// delivery), so we can react at every block boundary instead of
// pre-pumping ACKs blind. Saves ~25-40% wall-clock time on long
// dumps by removing the pump's idle gap between blocks.
//
// Flow:
//  1. Drain + switch transport into stream mode.
//  2. Send RSD.
//  3. Read 3-byte prefix; route handshake codes (NAK/abort) as errors.
//  4. Read remaining 16 header bytes, parse, send the post-header ACK
//     to release the first data block.
//  5. For each block: read 122 bytes (or a shorter partial last
//     block), decode + validate, send ACK.
//  6. Read trailing F7.
func (d *Device) getSampleAudioStreaming(pt transport.PartialRecvTransport, num byte, timeout time.Duration) (*protocol.SampleDumpHeader, []uint16, error) {
	pt.Drain()
	pt.BeginStream()
	defer pt.EndStream()

	if err := pt.Send(protocol.BuildRequestSampleDump(num)); err != nil {
		return nil, nil, fmt.Errorf("send RSD: %w", err)
	}
	ack := protocol.BuildHandshake(protocol.CodeACKS)
	deadline := time.Now().Add(timeout)

	// Read F0 7E XX. XX is either CodeSD (sample dump follows) or a
	// handshake code (the device declined the request).
	prefix := make([]byte, 3)
	if err := streamReadFull(pt, prefix, deadline); err != nil {
		return nil, nil, fmt.Errorf("read SDS prefix: %w", err)
	}
	if prefix[0] != protocol.SOX || prefix[1] != protocol.UniversalNRT {
		return nil, nil, fmt.Errorf("expected F0 7E, got % X", prefix[:2])
	}
	switch prefix[2] {
	case protocol.CodeSD:
		// fall through to header read
	case protocol.CodeNAKS:
		_, _ = pt.RecvBytes(make([]byte, 1), 200*time.Millisecond) // swallow trailing F7
		return nil, nil, errors.New("device NAK'd sample dump request")
	case protocol.CodeASD:
		_, _ = pt.RecvBytes(make([]byte, 1), 200*time.Millisecond)
		return nil, nil, errors.New("device aborted sample dump")
	default:
		return nil, nil, fmt.Errorf("unexpected SDS sub-code 0x%02X", prefix[2])
	}

	// Header is 19 bytes total (F0 7E 01 + 16 fields); we have 3,
	// pull the remaining 16.
	headerRest := make([]byte, 16)
	if err := streamReadFull(pt, headerRest, deadline); err != nil {
		return nil, nil, fmt.Errorf("read SDS header: %w", err)
	}
	fullHeader := append(prefix, headerRest...)
	hdr, err := protocol.ParseHeader(fullHeader)
	if err != nil {
		return nil, nil, fmt.Errorf("parse dump header: %w", err)
	}
	if hdr.Num != uint16(num) {
		return nil, nil, fmt.Errorf("dump is for slot %d, expected %d", hdr.Num, num)
	}
	totalNeeded := int(hdr.TotalWords)
	if totalNeeded < 0 {
		return nil, nil, fmt.Errorf("absurd total words: %d", totalNeeded)
	}
	// Post-header ACK releases the device's first data block.
	if err := pt.Send(ack); err != nil {
		return nil, nil, fmt.Errorf("send header ACK: %w", err)
	}

	words := make([]uint16, 0, totalNeeded)
	fullBlock := make([]byte, protocol.BlockSize)
	for len(words) < totalNeeded {
		remaining := totalNeeded - len(words)
		if remaining >= protocol.WordsPerBlock {
			if err := streamReadFull(pt, fullBlock, deadline); err != nil {
				return nil, nil, fmt.Errorf("read block %d: %w", len(words)/protocol.WordsPerBlock, err)
			}
			_, blockWords, derr := protocol.DecodeBlock(fullBlock)
			if derr != nil {
				return nil, nil, fmt.Errorf("decode block %d: %w", len(words)/protocol.WordsPerBlock, derr)
			}
			words = append(words, blockWords[:]...)
		} else {
			// Last block, remaining < 60. The S950 sends EITHER a
			// truncated `1 + remaining*2 + 1`-byte block OR a full
			// zero-padded 122-byte block — different firmware revs
			// (or maybe sample-size thresholds) behave differently.
			// Determine which by reading the partial-sized payload
			// first and peeking the next byte: F7 = truncated, any
			// other value = padded full block.
			partialSize := 1 + remaining*2 + 1
			if err := streamReadFull(pt, fullBlock[:partialSize], deadline); err != nil {
				return nil, nil, fmt.Errorf("read last block (partial bytes): %w", err)
			}
			peek := fullBlock[partialSize : partialSize+1]
			if err := streamReadFull(pt, peek, deadline); err != nil {
				return nil, nil, fmt.Errorf("read last block (peek): %w", err)
			}
			if peek[0] == protocol.EOX {
				// Truncated partial — F7 already consumed.
				blockWords, derr := decodePartialBlock(fullBlock[:partialSize])
				if derr != nil {
					return nil, nil, fmt.Errorf("decode partial last block: %w", derr)
				}
				words = append(words, blockWords...)
				if err := pt.Send(ack); err != nil {
					return nil, nil, fmt.Errorf("send final ACK: %w", err)
				}
				return hdr, words, nil
			}
			// Full padded block. The peek byte was real payload;
			// pull the rest of the 122-byte block and decode it.
			if err := streamReadFull(pt, fullBlock[partialSize+1:], deadline); err != nil {
				return nil, nil, fmt.Errorf("read last block (full padding): %w", err)
			}
			_, blockWords, derr := protocol.DecodeBlock(fullBlock)
			if derr != nil {
				return nil, nil, fmt.Errorf("decode last (full) block: %w", derr)
			}
			words = append(words, blockWords[:remaining]...)
		}
		if err := pt.Send(ack); err != nil {
			return nil, nil, fmt.Errorf("send block ACK: %w", err)
		}
	}

	// Trailing F7 closes the (one big) SysEx envelope.
	eox := make([]byte, 1)
	if err := streamReadFull(pt, eox, deadline); err != nil {
		return nil, nil, fmt.Errorf("read trailing F7: %w", err)
	}
	if eox[0] != protocol.EOX {
		return nil, nil, fmt.Errorf("expected F7, got 0x%02X", eox[0])
	}
	return hdr, words, nil
}

// streamReadFull fills buf entirely from a PartialRecvTransport's
// raw byte stream, respecting deadline. RecvBytes only guarantees ≥1
// byte per call, so we loop until satisfied.
func streamReadFull(pt transport.PartialRecvTransport, buf []byte, deadline time.Time) error {
	pos := 0
	for pos < len(buf) {
		left := time.Until(deadline)
		if left <= 0 {
			return fmt.Errorf("timeout (read %d/%d bytes)", pos, len(buf))
		}
		n, err := pt.RecvBytes(buf[pos:], left)
		if err != nil {
			return err
		}
		pos += n
	}
	return nil
}

// ackPumpInterval: pacing for the pre-emptive ACK pump.
const ackPumpInterval = 50 * time.Millisecond

// pumpAcks fires a 4-byte ACK (`F0 7E 7F F7`) immediately and then
// every ackPumpInterval until stop is closed. Errors are swallowed —
// the worst case is the device times out, which the caller will see
// as a missing dump envelope.
func (d *Device) pumpAcks(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ack := protocol.BuildHandshake(protocol.CodeACKS)
	_ = d.T.Send(ack) // fire one immediately so the header has an ACK waiting
	tick := time.NewTicker(ackPumpInterval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			if err := d.T.Send(ack); err != nil {
				return
			}
		}
	}
}

// headerEnvelopeBytes is the wire size of a header-only envelope:
// SOX + 7E + 01 + 16 header bytes + EOX = 20. The S950 sometimes
// flushes the header as its own envelope before the data blocks land
// (e.g. the rtmidi driver yielding on a brief lull); we discard those
// in favor of the larger envelope that carries the data blocks too.
const headerEnvelopeBytes = 20

// ParseSampleDumpEnvelope decodes an open-loop SDS envelope — the format
// PutSampleOpenLoop produces on the wire and the S950 sends back in response
// to RSD — into its header + a flat 12-bit words buffer. Pure function, no
// transport, fully unit-testable. `expectedSlot` is the slot we asked for;
// rejects misrouted dumps (catches bugs in busy multi-instrument MIDI setups
// where another device might reply to our RSD).
func ParseSampleDumpEnvelope(raw []byte, expectedSlot byte) (*protocol.SampleDumpHeader, []uint16, error) {
	if len(raw) < 20 {
		return nil, nil, fmt.Errorf("dump envelope too short: %d bytes", len(raw))
	}
	if raw[0] != protocol.SOX || raw[1] != protocol.UniversalNRT || raw[2] != protocol.CodeSD {
		return nil, nil, errors.New("not a sample dump envelope (bad F0 7E 01 prefix)")
	}
	if raw[len(raw)-1] != protocol.EOX {
		return nil, nil, errors.New("dump envelope missing trailing F7")
	}
	hdr, err := protocol.ParseHeader(raw[:19])
	if err != nil {
		return nil, nil, fmt.Errorf("parse dump header: %w", err)
	}
	if hdr.Num != uint16(expectedSlot) {
		return nil, nil, fmt.Errorf("dump is for slot %d, expected %d", hdr.Num, expectedSlot)
	}

	// Hardware behavior diverges from the spec doc on the final
	// block: the doc says "pad to 60 words with offset-binary
	// silence" but the S950 sometimes truncates the last block's
	// silence padding. Empirically: a 164060-word sample (which
	// needs ceil/60 = 2735 blocks) arrived as 333684 bytes — 6
	// bytes (3 SW words) short of the strict 333690 the spec
	// formula predicts. The first 2734 blocks are always full
	// 122-byte blocks; only the last one is variable. We walk the
	// body block-by-block from the start, decoding full blocks,
	// and handle the trailing partial block specially. Padding to
	// "true" word count comes from the header, not the wire body.
	body := raw[19 : len(raw)-1]
	totalNeeded := int(hdr.TotalWords)
	if totalNeeded < 0 {
		return nil, nil, fmt.Errorf("absurd total words: %d", totalNeeded)
	}
	words := make([]uint16, 0, totalNeeded)
	cursor := 0
	for cursor+protocol.BlockSize <= len(body) && len(words) < totalNeeded {
		blockWords, derr := decodeFixedBlock(body[cursor : cursor+protocol.BlockSize])
		if derr != nil {
			return nil, nil, fmt.Errorf("decode block at byte %d: %w", cursor, derr)
		}
		words = append(words, blockWords[:]...)
		cursor += protocol.BlockSize
	}
	// Trailing partial block (if any) — decode as many SW words as
	// the remaining bytes hold (after stripping the leading block#
	// and trailing checksum). Some firmware revs send less than a
	// full 60-word last block when the sample isn't a clean
	// multiple of 60 words.
	if rem := len(body) - cursor; rem > 2 && len(words) < totalNeeded {
		// Layout: 1 byte block# + N pairs (2 bytes each) + 1 byte checksum.
		swPairs := (rem - 2) / 2
		if swPairs > 0 {
			blockWords, derr := decodePartialBlock(body[cursor : cursor+1+swPairs*2+1])
			if derr != nil {
				return nil, nil, fmt.Errorf("decode trailing partial block (%d bytes): %w", rem, derr)
			}
			words = append(words, blockWords...)
		}
	}

	if len(words) < totalNeeded {
		return nil, nil, fmt.Errorf("dump short on words: got %d, header says %d", len(words), totalNeeded)
	}
	if len(words) > totalNeeded {
		words = words[:totalNeeded]
	}
	return hdr, words, nil
}

// decodeFixedBlock decodes a 122-byte standard block.
func decodeFixedBlock(block []byte) ([protocol.WordsPerBlock]uint16, error) {
	_, words, err := protocol.DecodeBlock(block)
	return words, err
}

// decodePartialBlock decodes a final block whose SW payload has
// fewer than 60 words. Layout: 1 byte block# + N*2 bytes SW data +
// 1 byte checksum. Checksum is XOR of the data bytes only (same
// scope as a full block), so we can validate the truncated frame
// the same way.
func decodePartialBlock(block []byte) ([]uint16, error) {
	if len(block) < 4 || (len(block)-2)%2 != 0 {
		return nil, fmt.Errorf("partial block %d bytes: not a valid 1+2N+1 layout", len(block))
	}
	swPairs := (len(block) - 2) / 2
	dataBytes := block[1 : 1+swPairs*2]
	checksum := block[len(block)-1]
	// XOR-checksum over the data bytes (matches Encode/DecodeBlock).
	var want byte
	for _, b := range dataBytes {
		want ^= b
	}
	if checksum != want {
		return nil, fmt.Errorf("partial block checksum 0x%02X != computed 0x%02X", checksum, want)
	}
	out := make([]uint16, swPairs)
	for i := 0; i < swPairs; i++ {
		out[i] = decodeSW(dataBytes[2*i], dataBytes[2*i+1])
	}
	return out, nil
}

// decodeSW mirrors protocol.DecodeSW without leaking that import
// alias here. The S950 stores 12-bit words split across two MIDI
// bytes: hi 7 bits in byte0[6:0], lo 5 bits in byte1[6:2].
func decodeSW(b0, b1 byte) uint16 {
	return (uint16(b0&0x7F) << 5) | (uint16(b1&0x7F) >> 2)
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
