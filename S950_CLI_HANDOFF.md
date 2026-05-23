# Akai S950 MIDI CLI — Build Handoff

**Purpose of this document:** a complete, self-contained spec for building a Go
command-line tool that manages samples and SysEx parameters on an Akai S950 (and
S900) digital sampler **over MIDI**. It is written to be handed to an AI coding
agent or developer. Everything needed to implement the protocol is here; you
should not need to re-derive byte layouts. Where something must be confirmed
against real hardware it is flagged **[VERIFY ON HARDWARE]**.

---

## 1. Project Overview

Build `s950`, a Go CLI that talks to an Akai S950 over a standard MIDI interface.

Core capabilities:

- List samples and programs stored on the device (catalog).
- Download a sample from the S950 to a WAV file.
- Upload a WAV file to the S950 as a sample.
- Read and write sample parameters (loop points, replay mode, tuning, etc.).
- Read and write programs/keygroups and overall settings.

Design goals: cross-platform (Linux/macOS/Windows), uses an ordinary 5-pin MIDI
interface (no RS-232), no external runtime dependencies beyond a MIDI backend.

---

## 2. Background and Key Decisions

**Why MIDI, not RS-232.** The S900/S950 sample-dump protocol is the Sequential
Prophet 2000 sample-dump format — the direct pre-SDS ancestor of the MIDI Sample
Dump Standard. MIDI is the *native* transport; RS-232 was an optional faster
alternative Akai added. The S950 selects its control source on the MIDI menu
(controller-select page): set it to **MIDI**, not RS-232C.

**It is NOT standard SDS.** The envelope is the same family as SDS (universal
non-realtime ID `0x7E`, sub-codes, `0x7C`–`0x7F` handshakes), but header length,
data-packet layout, and bit-packing differ. SDS devices and the S900/S950 cannot
interchange samples directly. Implement the Akai variant exactly as specified
below; do not assume an SDS library will work.

**Speed expectation.** MIDI is fixed at 31,250 baud (~3,125 bytes/s), moving
roughly 1,300–1,500 sample-words/second of audio. A 2-second 22 kHz loop takes
~30 s; near-full memory takes ~5 minutes. This is acceptable and intentional.

**Primary reference document.** Akai's "MIDI System Exclusive Data Format for
S900, Disk Version V2.0" — scanned copy at
`https://archive.org/details/Akai_S900_SysEx_format`. It applies to the S950
(shared implementation). All byte layouts below are derived from it.

**Secondary reference.** `https://github.com/dxzl/akai-s950` — a working C++
RS-232 implementation. Useful to cross-check S950-specific interpretations of
fields the S900 doc marks "undefined/reserved." Its protocol logic
(encode/decode, checksum, handshake state machine) is directly portable; only its
transport layer is RS-232-specific.

---

## 3. Hardware Setup / Preconditions

- The S950 must be set to **MIDI** control on the MIDI-menu controller-select
  page (equivalently the `MIRS2` field of overall settings: 1 = MIDI,
  2 = RS-232C).
- A working 5-pin MIDI interface, computer MIDI OUT → S950 MIDI IN and
  S950 MIDI OUT → computer MIDI IN (both directions required for catalog
  requests, downloads, and closed-loop handshaking).
- Avoid USB-MIDI interfaces with very small SysEx buffers. The sample dump is
  transmitted as one long SysEx message; the interface and driver must handle
  long SysEx in both directions. **Validate this early** (see Milestone 1).

---

## 4. Protocol Specification

### 4.1 Transport and framing

- All communication is MIDI System Exclusive. Every byte between `F0` and `F7`
  is a 7-bit data byte (`0x00`–`0x7F`).
- Two message families:
  - **AKAI exclusive** — `F0 47 <chan> <func> 40 <num> 00 ... F7`. Carries a
    manufacturer ID (`0x47`) and a MIDI channel. Used for catalog, programs,
    sample parameters, overall/drum settings.
  - **System-exclusive-common** — `F0 7E <code> ... F7`. No manufacturer ID,
    no channel. Used for the sample dump itself, dump requests, and handshakes.
    Because these are unaddressed, on a multi-sampler bus every sampler responds;
    use `SECRE`/`SECRD` (section 4.4) to enable/disable a specific unit. Not a
    concern for a single-S950 rig.
- **Critical framing rule:** an entire sample dump (header + all data blocks) is
  **one SysEx message** — a single `F0` at the very start and a single `F7`
  *only after the last data block*. Data blocks have no `F0`/`F7` of their own.
  Do not terminate the SysEx between blocks.

### 4.2 Data encoding primitives

The doc uses six type codes. Implement each as a pure function with round-trip
unit tests.

| Code | Meaning | Wire size |
|------|---------|-----------|
| `B`  | raw 7-bit byte | 1 MIDI byte |
| `DB` | 8-bit value | 2 MIDI bytes (low 7 bits, then bit 7) |
| `DW` | 16-bit value | 4 MIDI bytes (two `DB`, little-endian) |
| `DD` | 32-bit value | 8 MIDI bytes (four `DB`, little-endian) |
| `TB` | 21-bit value | 3 MIDI bytes (little-endian 7-bit groups) |
| `SW` | 12-bit sample word | 2 MIDI bytes (special packing, offset binary) |
| `DB*`| 10 ASCII chars | 20 MIDI bytes (10 × `DB`) |

Reference Go implementations:

```go
// SW — 12-bit sample word. Stored offset-binary: silence = 0x800.
// Wire layout: byte0 = 0 d11..d5, byte1 = 0 d4..d0 0 0.
func EncodeSW(w uint16) (b0, b1 byte) {
	w &= 0x0FFF
	return byte((w >> 5) & 0x7F), byte((w << 2) & 0x7F)
}
func DecodeSW(b0, b1 byte) uint16 {
	return (uint16(b0&0x7F) << 5) | (uint16(b1&0x7F) >> 2)
}

// TB — 21-bit, little-endian 7-bit groups.
func EncodeTB(v uint32) [3]byte {
	return [3]byte{byte(v & 0x7F), byte((v >> 7) & 0x7F), byte((v >> 14) & 0x7F)}
}
func DecodeTB(b [3]byte) uint32 {
	return uint32(b[0]&0x7F) | uint32(b[1]&0x7F)<<7 | uint32(b[2]&0x7F)<<14
}

// DB — one 8-bit byte as low-7-bits + bit-7.
func EncodeDB(v byte) (lo, hi byte) { return v & 0x7F, (v >> 7) & 0x01 }
func DecodeDB(lo, hi byte) byte     { return (lo & 0x7F) | ((hi & 0x01) << 7) }

// DW — 16-bit, two DB groups, little-endian.
func EncodeDW(v uint16) [4]byte {
	l0, h0 := EncodeDB(byte(v))
	l1, h1 := EncodeDB(byte(v >> 8))
	return [4]byte{l0, h0, l1, h1}
}

// DD — 32-bit, four DB groups, little-endian.
func EncodeDD(v uint32) [8]byte {
	var out [8]byte
	for i := 0; i < 4; i++ {
		lo, hi := EncodeDB(byte(v >> (8 * uint(i))))
		out[2*i], out[2*i+1] = lo, hi
	}
	return out
}
```

Known test vector: `EncodeSW(2048)` must yield `0x40, 0x00` (the doc states
"zero is sent as 40 00H"). `EncodeSW(4095)` → `0x7F, 0x7C`.

### 4.3 Checksums

- **Sample data block checksum** — XOR of the **120 data bytes only** (not the
  block-number byte that precedes them).
- **AKAI exclusive message checksum** — XOR of every data byte **after the
  7-byte header** (`F0 47 chan func 40 num 00`), i.e. excluding bytes 0–6, and
  excluding the checksum and `F7` themselves.
- The sample-dump **header has no checksum**.

```go
func XorChecksum(data []byte) byte {
	var c byte
	for _, b := range data {
		c ^= b
	}
	return c & 0x7F
}
```

### 4.4 Message catalog

**AKAI-exclusive function codes** (byte 3):

| Code | Name  | Direction | Meaning |
|------|-------|-----------|---------|
| 0 | `RDRS`  | → S950 | request drum settings |
| 1 | `ROVS`  | → S950 | request overall settings |
| 2 | `RPRGM` | → S950 | request program + keygroups |
| 3 | `RCAT`  | → S950 | request program/sample name catalog |
| 4 | `RSPRM` | → S950 | request sample parameters |
| 5 | `SECRE` | → S950 | system-exclusive-common reception **enable** |
| 6 | `SECRD` | → S950 | system-exclusive-common reception **disable** |
| 7 | `DRS`   | both   | drum settings (data) |
| 8 | `OVS`   | both   | overall settings (data) |
| 9 | `PRGM`  | both   | program + keygroups (data) |
| 10| `SPRM`  | both   | sample parameters (data) |
| 11| `CAT`   | S950 → | name catalog (data) |

To **request**, send a code 0–4. The S950 **responds** with the corresponding
data message (codes 7–11). To **write** settings/program/params to the S950,
send a code 7–11 with the data payload.

**AKAI-exclusive request message** (8 bytes):

```
F0 47 <chan> <func> 40 <num> 00 F7
        |       |     |    |
        |       |     |    sample/program number (0 if N/A)
        |       |     S950 device identifier (0x40 = 64)
        |       function code (table above)
        MIDI channel 0–15
```

`<num>` is the sample or program number (single 7-bit byte; S950 supports up to
~99 samples — fits). Examples:
- Catalog request: `F0 47 00 03 40 00 00 F7`
- Sample N parameters request: `F0 47 00 04 40 <N> 00 F7`

**System-exclusive-common messages.** Byte 2 is a code:

| Byte2 | Name | Message |
|-------|------|---------|
| `0x00` | `RSD`  | request sample dump |
| `0x01` | `SD`   | sample dump (header + blocks) |
| `0x7D` | `ASD`  | abort sample dump |
| `0x7E` | `NAKS` | not-acknowledge / request retransmit |
| `0x7F` | `ACKS` | acknowledge block or header |

- Request sample dump (6 bytes): `F0 7E 00 <num> 00 F7`
- Handshake (4 bytes): `F0 7E <0x7D|0x7E|0x7F> F7`
- Sample dump: see 4.5.

### 4.5 Sample dump format

The whole dump is **one SysEx**. Structure on the wire:

```
F0 7E 01                          <- start + SD code
<header bytes 5..18, see below>   <- 14 more bytes; total header is 19 bytes
<block 0>                         <- 122 bytes
<block 1>                         <- 122 bytes
...
<block N>                         <- 122 bytes (last block zero-padded)
F7                                <- single EOX, only here
```

**Header (19 bytes total, byte indices 0–18):**

| Byte | Type | Field | Notes |
|------|------|-------|-------|
| 0 | B | `0xF0` | start of SysEx |
| 1 | B | `0x7E` | common non-realtime ID |
| 2 | B | `0x01` | `SD` sample-dump code |
| 3 | B | sample number LSB | 0–127 |
| 4 | B | sample number MSB | 0 for S950 (≤99 samples) |
| 5 | B | bits per word | S950 sends 12; accepts 8–16. **Use 12.** |
| 6–8  | TB | sampling period (ns) | range 15259–500000 |
| 9–11 | TB | total words in sample | range 200–475020 |
| 12–14| TB | loop start point | relative to start of sample |
| 15–17| TB | loop end point | S950 treats this as the end point |
| 18 | B | mode | 0 = looping, 1 = alternating |

One-shot playback: indicated when loop length < 5 — set loop start ≥
`total_words − 5` (the doc: "If loop start is equal to or larger than TOTAL
WORDS − 5, assume non-looping").

**Data block (122 bytes), repeated:**

| Offset | Type | Field |
|--------|------|-------|
| 0 | B | block number LSB (`block & 0x7F`; MSB is not sent — it wraps at 128) |
| 1–120 | 60 × SW | sample data — 60 twelve-bit words |
| 121 | B | checksum = XOR of the 120 data bytes |

Number of blocks = `ceil(total_words / 60)`. **[VERIFY ON HARDWARE]** Pad the
final block to a full 60 words with offset-binary silence (`0x40 0x00`); the
receiver uses the header's `total words` field to know the true length.

**Closed-loop handshaking.** The S950 is always a slave and acknowledges the
header and each block with `F0 7E 7F F7` (ACK), `F0 7E 7E F7` (NAK → retransmit
that block), or `F0 7E 7D F7` (abort). The doc explicitly allows up to **10
seconds** between blocks in closed-loop mode, so MIDI's lack of hardware flow
control is irrelevant — the per-block ACK is the flow control.

**Open-loop vs closed-loop (important implementation choice):**

- *Open loop*: send the entire dump as one SysEx with no waiting. Simplest;
  recommended for v1 uploads. The S950 can receive one-way.
- *Closed loop*: wait for an ACK after the header and after each block; resend on
  NAK. More reliable, and **required for downloads** (the S950 pauses for your
  ACK after each block it sends). The complication: between blocks the SysEx is
  "paused" — `F0` has been sent, `F7` has not — while a short ACK SysEx travels
  the other direction. Your MIDI layer must therefore support sending and
  receiving a SysEx **incrementally / in chunks**, not only as one atomic
  message. Plan the transport interface around this from the start (section 6).

### 4.6 Sample parameter block (`SPRM`, function 10)

Full message: `F0 47 <chan> 0A 40 <num> 00 <120 param bytes> <checksum> F7`
(129 bytes total). The 120-byte parameter block (doc byte offsets are *within
the message*, starting at message byte 7):

| Msg bytes | Type | Field | Meaning |
|-----------|------|-------|---------|
| 7–26   | DB* | `SNAME`  | sample name, 10 ASCII chars |
| 27–34  | DD  | —        | undefined (preserve on round-trip) |
| 35–38  | DW  | —        | undefined (preserve) |
| 39–46  | DD  | `SLNGTH` | total words in sample |
| 47–50  | DW  | `SMRATE` | sample rate in Hz |
| 51–54  | DW  | `SNOMP`  | nominal pitch, 1/16 semitone, C3 = 960 |
| 55–58  | DW  | `SDFLDO` | loudness offset, signed |
| 59–60  | DB  | `SRPLMD` | replay mode: 'O' one-shot, 'L' loop, 'A' alt |
| 61–62  | DB  | —        | reserved |
| 63–70  | DD  | `SEND`   | end point relative to sample start |
| 71–78  | DD  | `SSTART` | first replay point relative to start |
| 79–86  | DD  | `SLOOP`  | loop / alternating length |
| 87–90  | DW  | —        | reserved (S950 uses this — preserve) |
| 91–92  | DB  | `VC`     | 0 = normal, 255 = velocity crossfade |
| 93–94  | DB  | `NOREV`  | 'N' normal, 'R' reversed waveform |
| 95–126 | 4×DD| —        | undefined (S950 uses some — preserve) |

Checksum at byte 127, `F7` at byte 128.

**Round-trip rule for `set-params`:** read the current block, modify only the
fields the user changed, write the whole block back **unchanged elsewhere**.
Several fields the S900 doc calls "undefined/reserved" are used by the S950
(dxzl's source annotates the offsets 87, 95, 103, 111, 119). Never zero them.

### 4.7 Programs, overall settings, drum settings

These follow the same `F0 47 <chan> <func> 40 <num> 00 <data> <checksum> F7`
shape. Sizes from the reference doc:

- **Overall settings** (`OVS`, func 8): 80 data bytes. Includes the controller
  selector `MIRS2` (1 = MIDI / 2 = RS-232C), basic MIDI channel, pitch-wheel
  range, RS-232 baud, current program name.
- **Program + keygroups** (`PRGM`, func 9): a 76-byte program header followed by
  1–32 keygroup blocks of 140 bytes each, then checksum + `F7`.
- **Drum settings** (`DRS`, func 7): 480 data bytes (eight 60-byte input blocks).

Full keygroup field maps are in the reference document, section "AKAI EXCLUSIVE
program." Implement programs/overall settings after samples are working; for v1
it is acceptable to treat program/overall/drum payloads as opaque
read-modify-write byte blocks with named accessors added incrementally.

---

## 5. Sample Data Conversion (WAV ↔ S950)

The S950 is a 12-bit sampler. WAV input is normally 16-bit signed mono PCM.

```go
// 16-bit signed PCM -> 12-bit offset-binary S950 word.
func PCM16toSW(s int16) uint16 {
	v := int32(s) >> 4 // 16-bit signed -> 12-bit signed (arithmetic shift)
	v += 2048          // offset binary: silence = 0x800
	if v < 0 {
		v = 0
	}
	if v > 4095 {
		v = 4095
	}
	return uint16(v)
}

// S950 word -> 16-bit signed PCM.
func SWtoPCM16(w uint16) int16 {
	return int16((int32(w&0x0FFF) - 2048) << 4)
}
```

- **Mono only.** Reject or downmix stereo input.
- **Sample rate.** The S950 stores a *period in nanoseconds*:
  `period_ns = round(1e9 / sample_rate_hz)`. Valid range 15259–500000 ns
  (≈ 2 kHz–65 kHz). If the WAV's rate is in range, keep it and just record the
  period — no resampling needed. If out of range, resample or clamp and warn.
- **Loop points.** Read from the WAV `smpl` chunk if present; otherwise default
  to one-shot (loop start ≥ total − 5). Map to header fields and `SPRM`
  `SSTART`/`SEND`/`SLOOP`/`SRPLMD`.
- **Length limits.** Minimum 200 words, maximum 475020 words. Validate.

---

## 6. Recommended Architecture

Suggested layout:

```
s950/
  cmd/s950/main.go         CLI entrypoint (cobra)
  internal/sysex/          encoding primitives (B/DB/DW/DD/TB/SW), checksum
  internal/protocol/       message builders + parsers (catalog, params,
                           dump header, blocks, handshakes)
  internal/transport/      MIDI abstraction: interface + concrete driver
  internal/sample/         WAV <-> S950 conversion, sample model
  internal/device/         high-level ops: Catalog, GetSample, PutSample,
                           GetParams, SetParams, GetProgram, ...
  internal/sysex/*_test.go codec round-trip tests with known vectors
```

**Libraries:**

- MIDI: `gitlab.com/gomidi/midi/v2` with the `rtmididrv` driver (CGo + rtmidi).
  **Verify before committing:** that the chosen driver can (a) send a long SysEx
  and (b) send/receive a SysEx incrementally for closed-loop mode. If the
  high-level API only handles atomic SysEx, drop to the driver's raw send/listen.
  Fallbacks worth keeping in mind: direct `rtmidi` CGo bindings, or shelling out
  to `amidi` on Linux for a first prototype.
- CLI: `github.com/spf13/cobra` (or stdlib `flag` if you prefer minimal deps).
- WAV: `github.com/go-audio/wav` + `github.com/go-audio/audio`.

**Transport interface** — design it to support chunked I/O from day one:

```go
type Transport interface {
	// Send arbitrary raw MIDI bytes (may be a partial SysEx).
	Send(b []byte) error
	// Receive the next complete inbound SysEx (F0..F7), or time out.
	RecvSysEx(timeout time.Duration) ([]byte, error)
	Close() error
}
```

For closed-loop transfers you will also want a way to read inbound messages
while an outbound SysEx is still open; keep that in the interface or expose the
raw inbound byte stream.

**Suggested commands:**

```
s950 ports                                   list MIDI in/out ports
s950 catalog                                 list samples + programs on device
s950 get-sample <num> <out.wav>               download a sample
s950 put-sample <in.wav> [--slot N] [--name]  upload a sample
s950 get-params <num> [--json]                read sample parameters
s950 set-params <num> [--loop-start ...] ...  modify sample parameters
s950 get-program <num> / set-program ...      programs (later milestone)
s950 get-overall / set-overall ...            overall settings (later)
```

Global flags: `--in <port>`, `--out <port>`, `--channel <0-15>`,
`--timeout <dur>`, `--open-loop` / `--closed-loop`, `--verbose` (hex-dump SysEx).

---

## 7. Implementation Plan / Milestones

Build in this order — each milestone de-risks the next.

1. **MIDI plumbing + `ports` + `catalog`.** Send `RCAT`, parse the returned
   `CAT` message (12-byte entries: type byte 'P'/'S', number, 10 ASCII chars).
   This proves port I/O, SysEx framing in both directions, and the device's
   controller-mode setting. Lowest-risk full round trip.
2. **`get-params`.** Send `RSPRM`, decode the 120-byte block, pretty-print
   (and `--json`). Read-only, exercises every decoding primitive.
3. **`get-sample`.** Send `RSD`, receive the dump, decode `SW` data, write a
   16-bit WAV. First real transfer; implement closed-loop receive (ACK each
   block) here.
4. **`put-sample` (open-loop).** Convert WAV → 12-bit, build header + blocks,
   send as one SysEx. Simplest upload path.
5. **Closed-loop upload + retransmit.** Add ACK/NAK handling and per-block
   retry for reliability on long samples.
6. **`set-params`, programs, overall settings.** Read-modify-write with field
   accessors; preserve all unknown bytes.

---

## 8. Known Pitfalls and Gotchas

- **Device must be in MIDI control mode** (MIDI menu controller-select page).
  If it is on RS-232C it will ignore everything.
- **One SysEx per dump.** `F0` at the very start, `F7` only after the last
  block. Do not emit `F7` between blocks.
- **`SW` is offset binary**, 12-bit, silence = `0x800`. Pack exactly as in 4.2.
- **Block checksum scope** = the 120 data bytes only, *not* the block-number
  byte. AKAI-exclusive checksum = all bytes after the 7-byte header.
- **Block numbers wrap at 128** (only the 7-bit LSB is transmitted).
- **Preserve "undefined/reserved" fields** on any read-modify-write — the S950
  uses several of them even though the S900 doc does not document them.
- **16→12-bit conversion** is an arithmetic right shift by 4, then `+2048`
  offset, then clamp to 0–4095.
- **Sample-rate is stored as a period in ns** in the dump header; convert and
  range-check (15259–500000 ns).
- **Timeouts:** for AKAI-exclusive messages, no more than ~2 s between bytes
  after the first six (the S950 will abort an incomplete message). For
  closed-loop dumps, up to 10 s between blocks is allowed.
- **Long-SysEx support is the riskiest dependency.** Confirm your MIDI driver
  handles long SysEx send *and* receive, and chunked SysEx for closed loop,
  before building higher layers on it.
- **Multi-sampler buses:** system-exclusive-common messages are unaddressed; use
  `SECRE`/`SECRD` to target one unit. Irrelevant for a single S950.
- **Final block padding** to 60 words: **[VERIFY ON HARDWARE]** — confirm the
  S950 accepts a zero-padded final block and relies on the header word count.

---

## 9. Testing Strategy

- **Codec unit tests.** Round-trip every primitive (`DB/DW/DD/TB/SW`) over
  random inputs; assert known vectors (`EncodeSW(2048) == {0x40,0x00}`,
  `EncodeSW(4095) == {0x7F,0x7C}`). Test checksum against hand-computed cases.
- **Dry-run / hex-dump mode.** A `--verbose` or `--dry-run` flag that prints the
  exact SysEx bytes the tool *would* send. Eyeball-verify message framing before
  touching hardware.
- **Golden fixtures.** Capture real S950 output (catalog, sample parameters, a
  short sample dump) with a MIDI monitor — `amidi -d` on Linux, MIDI-OX on
  Windows, or SysEx Librarian on macOS — and commit the byte dumps as parser
  test fixtures.
- **Integration order.** Catalog round-trip first; then parameter read; then a
  short sample (a few hundred words) download and re-upload, comparing audio.

---

## 10. References

- **Akai S900 MIDI System Exclusive Data Format, V2.0** — authoritative byte
  spec, applies to the S950: `https://archive.org/details/Akai_S900_SysEx_format`
  (use the full-text or PDF download).
- **dxzl/akai-s950** — working C++/RS-232 implementation; cross-reference for
  S950-specific field interpretations: `https://github.com/dxzl/akai-s950`.
- **S950 Operator's Manual addendum** — front-panel sample-dump initiation and
  the MIDI/RS-232 controller switch.
- **gomidi/midi v2** — `https://gitlab.com/gomidi/midi`.

---

*End of handoff. Implement section 4 exactly; treat 6–9 as guidance. Flag any
hardware behavior that contradicts this document and update section 4
accordingly.*
