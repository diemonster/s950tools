// Package protocol builds and parses S900/S950 MIDI SysEx messages.
//
// Two families coexist on the wire:
//
//   - AKAI exclusive:    F0 47 <chan> <func> 40 <num> 00 ... <cksum> F7
//   - System-exclusive-common (SDS-like): F0 7E <code> ... F7
//
// All multi-byte payload fields use the sysex package's encodings.
package protocol

import (
	"errors"
	"fmt"

	"github.com/bivers/s950/internal/sysex"
)

// AkaiMessage is a decoded AKAI-exclusive SysEx message
// (F0 47 <chan> <func> 40 <num> 00 ... <cksum> F7).
type AkaiMessage struct {
	// Channel is the MIDI channel taken from byte 2 (low 4 bits).
	Channel byte
	// Function is the AKAI function code from byte 3 (one of FuncRDRS..FuncCAT).
	Function byte
	// Num is the sample/program slot byte from byte 5; 0 for messages that
	// don't address a particular slot.
	Num byte
	// Payload is the body of the message: every byte between the 7-byte
	// header and the trailing checksum.
	Payload []byte
}

// CatalogEntry is one record from a CAT message (function 11): a
// single-character type plus its slot number and trimmed name.
type CatalogEntry struct {
	// Type is 'P' for program entries or 'S' for sample entries.
	Type byte
	// Num is the 1-byte slot index in the S950's program/sample table.
	Num byte
	// Name is the trimmed (no trailing space/NUL) display name from the entry.
	Name string
}

// SampleParams represents the high-level fields of an SPRM (function 10)
// payload. The raw 120-byte block is preserved in Raw so unknown/reserved
// fields can be written back unchanged.
type SampleParams struct {
	// Raw is the full 120-byte SPRM payload as received. EncodePayload uses
	// this as the base so any unmodelled fields round-trip untouched.
	Raw [120]byte

	// Name is the 10-character sample name (SNAME, payload bytes 0..19).
	Name string
	// TotalWords is the sample length in 12-bit words (SLNGTH).
	TotalWords uint32
	// SampleRateHz is the playback sample rate in Hz (SMRATE).
	SampleRateHz uint16
	// NominalPitch is the sample's pitch in 1/16-semitone SNOMP units (SNOMP).
	// C3 = 960.
	NominalPitch uint16
	// LoudOffset is the loudness offset (SDFLDO), signed.
	LoudOffset int16
	// ReplayMode selects looping behaviour (SRPLMD): ReplayOneShot,
	// ReplayLoop, or ReplayAlternating.
	ReplayMode byte
	// End is the sample end point in words (SEND).
	End uint32
	// Start is the sample start point in words (SSTART).
	Start uint32
	// LoopLength is the loop length in words (SLOOP).
	LoopLength uint32
	// VelXFade is the velocity-crossfade flag (VC).
	VelXFade byte
	// Reversed is the playback-direction flag (NOREV): 'N' = forward,
	// 'R' = reverse.
	Reversed byte
}

// SampleDumpHeader is the 19-byte SDS-style header that precedes the sample
// data blocks in a sample dump exchange.
type SampleDumpHeader struct {
	// Num is the sample slot number (LSB+MSB on the wire; S950 uses 0..99).
	Num uint16
	// BitsPerWord is the sample word size. The S950 sends 12; it accepts 8..16.
	BitsPerWord byte
	// PeriodNS is the sampling period in nanoseconds. Doc range 15259..500000
	// (~2 kHz..~65 kHz).
	PeriodNS uint32
	// TotalWords is the total sample length in words. Doc range 200..475020.
	TotalWords uint32
	// LoopStart is the loop start offset in words, relative to the sample start.
	LoopStart uint32
	// LoopEnd is the loop end offset in words, relative to the sample start.
	// Per the doc this is actually used as the playback end point.
	LoopEnd uint32
	// Mode selects loop behaviour: 0 = looping, 1 = alternating.
	Mode byte
}

// AKAI manufacturer ID and the S950 device identifier byte.
const (
	ManufacturerAKAI byte = 0x47
	DeviceIDS950     byte = 0x40 // S900/S950 identifier byte (decimal 64)
	UniversalNRT     byte = 0x7E // System-exclusive-common ID
)

// AKAI function codes (byte 3 of an AKAI exclusive message).
//
// TODO(delete-sample): the documented S900/S950 SysEx surface has no
// delete-sample / delete-program opcode — the front-panel DELETE
// functions appear to be local-only. Before exposing a Delete action
// in the UI, check dxzl/akai-s950 for any undocumented opcode the
// V2.0 spec doesn't list, and verify on hardware.
const (
	FuncRDRS  byte = 0  // request drum settings
	FuncROVS  byte = 1  // request overall settings
	FuncRPRGM byte = 2  // request program + keygroups
	FuncRCAT  byte = 3  // request program/sample name catalog
	FuncRSPRM byte = 4  // request sample parameters
	FuncSECRE byte = 5  // system-exclusive-common reception enable
	FuncSECRD byte = 6  // system-exclusive-common reception disable
	FuncDRS   byte = 7  // drum settings (data)
	FuncOVS   byte = 8  // overall settings (data)
	FuncPRGM  byte = 9  // program + keygroups (data)
	FuncSPRM  byte = 10 // sample parameters (data)
	FuncCAT   byte = 11 // name catalog (data)
)

// System-exclusive-common subcodes (byte 2).
const (
	CodeRSD  byte = 0x00 // request sample dump
	CodeSD   byte = 0x01 // sample dump
	CodeASD  byte = 0x7D // abort sample dump
	CodeNAKS byte = 0x7E // not-acknowledge / retransmit
	CodeACKS byte = 0x7F // acknowledge
)

// SOX / EOX markers.
const (
	SOX byte = 0xF0
	EOX byte = 0xF7
)

// ReplayMode constants (ASCII letters per the doc).
const (
	ReplayOneShot     byte = 'O'
	ReplayLoop        byte = 'L'
	ReplayAlternating byte = 'A'
)

// WordsPerBlock is the fixed number of 12-bit words per sample-dump data block.
const WordsPerBlock = 60

// BlockSize is the on-wire size of one block: 1 number + 60*2 data + 1 checksum.
const BlockSize = 1 + 2*WordsPerBlock + 1 // 122

// BuildAkaiRequest returns the 8-byte request message:
//
//	F0 47 <chan> <func> 40 <num> 00 F7
//
// num is 0 for messages that don't address a specific sample/program.
func BuildAkaiRequest(channel, function, num byte) []byte {
	return []byte{
		SOX,
		ManufacturerAKAI,
		channel & 0x0F,
		function & 0x7F,
		DeviceIDS950,
		num & 0x7F,
		0x00,
		EOX,
	}
}

// BuildAkaiData wraps a payload (the bytes between the 7-byte header and the
// trailing checksum + EOX) into a full AKAI exclusive data message.
//
// The checksum is the XOR of every payload byte (i.e. everything after the
// 7-byte header), per the doc.
func BuildAkaiData(channel, function, num byte, payload []byte) []byte {
	out := make([]byte, 0, 7+len(payload)+2)
	out = append(out,
		SOX,
		ManufacturerAKAI,
		channel&0x0F,
		function&0x7F,
		DeviceIDS950,
		num&0x7F,
		0x00,
	)
	out = append(out, payload...)
	out = append(out, sysex.XorChecksum(payload))
	out = append(out, EOX)
	return out
}

// BuildRequestSampleDump returns the 6-byte RSD message: F0 7E 00 <num> 00 F7.
func BuildRequestSampleDump(num byte) []byte {
	return []byte{SOX, UniversalNRT, CodeRSD, num & 0x7F, 0x00, EOX}
}

// BuildHandshake returns a 4-byte handshake message: F0 7E <code> F7.
// code must be CodeACKS, CodeNAKS, or CodeASD.
func BuildHandshake(code byte) []byte {
	return []byte{SOX, UniversalNRT, code, EOX}
}

// ParseAkai decodes a complete AKAI-exclusive SysEx message starting with F0
// and ending with F7. It validates structure and checksum.
func ParseAkai(buf []byte) (*AkaiMessage, error) {
	if len(buf) < 9 {
		return nil, fmt.Errorf("AKAI message too short: %d bytes", len(buf))
	}
	if buf[0] != SOX || buf[len(buf)-1] != EOX {
		return nil, errors.New("AKAI message missing SOX/EOX")
	}
	if buf[1] != ManufacturerAKAI {
		return nil, fmt.Errorf("not an AKAI message: id=0x%02X", buf[1])
	}
	if buf[4] != DeviceIDS950 {
		return nil, fmt.Errorf("unexpected device id 0x%02X", buf[4])
	}
	payload := buf[7 : len(buf)-2]
	got := buf[len(buf)-2]
	want := sysex.XorChecksum(payload)
	if got != want {
		return nil, fmt.Errorf("checksum mismatch: got 0x%02X want 0x%02X", got, want)
	}
	return &AkaiMessage{
		Channel:  buf[2] & 0x0F,
		Function: buf[3] & 0x7F,
		Num:      buf[5] & 0x7F,
		Payload:  payload,
	}, nil
}

// IsHandshake reports whether buf is one of the 4-byte common handshakes and
// returns the code. Anything else returns (false, 0).
func IsHandshake(buf []byte) (bool, byte) {
	if len(buf) != 4 || buf[0] != SOX || buf[3] != EOX || buf[1] != UniversalNRT {
		return false, 0
	}
	switch buf[2] {
	case CodeACKS, CodeNAKS, CodeASD:
		return true, buf[2]
	}
	return false, 0
}

// ParseCatalog decodes the payload of a CAT message (function 11) into entries.
// Each on-wire entry is 12 bytes: type, num, then 10 ASCII chars
// (the doc states the catalog name bytes are sent as plain ASCII, not
// DB-encoded).
func ParseCatalog(payload []byte) ([]CatalogEntry, error) {
	const entrySize = 12
	if len(payload)%entrySize != 0 {
		return nil, fmt.Errorf("CAT payload length %d not a multiple of %d", len(payload), entrySize)
	}
	out := make([]CatalogEntry, 0, len(payload)/entrySize)
	for i := 0; i < len(payload); i += entrySize {
		raw := payload[i+2 : i+12]
		// Trim trailing spaces / NULs.
		n := len(raw)
		for n > 0 && (raw[n-1] == ' ' || raw[n-1] == 0) {
			n--
		}
		out = append(out, CatalogEntry{
			Type: payload[i],
			Num:  payload[i+1],
			Name: string(raw[:n]),
		})
	}
	return out, nil
}

// Offsets within the 120-byte SPRM payload (the message's bytes 7..126 mapped
// to 0..119). Derived from the reference doc, section "AKAI EXCLUSIVE SAMPLE
// PARAMETER," with the S900 doc's message-relative offsets re-based to 0..119.
const (
	offSNAME  = 0  // 20 bytes (DB* — 10 chars)
	offSLNGTH = 32 // 8 bytes (DD)
	offSMRATE = 40 // 4 bytes (DW)
	offSNOMP  = 44 // 4 bytes (DW)
	offSDFLDO = 48 // 4 bytes (DW, signed)
	offSRPLMD = 52 // 2 bytes (DB)
	offSEND   = 56 // 8 bytes (DD)
	offSSTART = 64 // 8 bytes (DD)
	offSLOOP  = 72 // 8 bytes (DD)
	offVC     = 84 // 2 bytes (DB)
	offNOREV  = 86 // 2 bytes (DB)
)

// ParseSampleParams decodes an SPRM payload.
func ParseSampleParams(payload []byte) (*SampleParams, error) {
	if len(payload) != 120 {
		return nil, fmt.Errorf("SPRM payload must be 120 bytes, got %d", len(payload))
	}
	var raw [120]byte
	copy(raw[:], payload)

	p := &SampleParams{Raw: raw}
	p.Name = sysex.DecodeName(*(*[20]byte)(payload[offSNAME : offSNAME+20]))
	p.TotalWords = sysex.DecodeDD(*(*[8]byte)(payload[offSLNGTH : offSLNGTH+8]))
	p.SampleRateHz = sysex.DecodeDW(*(*[4]byte)(payload[offSMRATE : offSMRATE+4]))
	p.NominalPitch = sysex.DecodeDW(*(*[4]byte)(payload[offSNOMP : offSNOMP+4]))
	p.LoudOffset = int16(sysex.DecodeDW(*(*[4]byte)(payload[offSDFLDO : offSDFLDO+4])))
	p.ReplayMode = sysex.DecodeDB(payload[offSRPLMD], payload[offSRPLMD+1])
	p.End = sysex.DecodeDD(*(*[8]byte)(payload[offSEND : offSEND+8]))
	p.Start = sysex.DecodeDD(*(*[8]byte)(payload[offSSTART : offSSTART+8]))
	p.LoopLength = sysex.DecodeDD(*(*[8]byte)(payload[offSLOOP : offSLOOP+8]))
	p.VelXFade = sysex.DecodeDB(payload[offVC], payload[offVC+1])
	p.Reversed = sysex.DecodeDB(payload[offNOREV], payload[offNOREV+1])

	return p, nil
}

// EncodePayload re-encodes the high-level fields back into a 120-byte payload,
// preserving every unmodified byte from Raw. This is the safe path for any
// read-modify-write because several "undefined/reserved" S900 fields are used
// by the S950.
func (p *SampleParams) EncodePayload() []byte {
	out := make([]byte, 120)
	copy(out, p.Raw[:])

	name := sysex.EncodeName(p.Name)
	copy(out[offSNAME:offSNAME+20], name[:])

	sl := sysex.EncodeDD(p.TotalWords)
	copy(out[offSLNGTH:offSLNGTH+8], sl[:])
	sr := sysex.EncodeDW(p.SampleRateHz)
	copy(out[offSMRATE:offSMRATE+4], sr[:])
	np := sysex.EncodeDW(p.NominalPitch)
	copy(out[offSNOMP:offSNOMP+4], np[:])
	ld := sysex.EncodeDW(uint16(p.LoudOffset))
	copy(out[offSDFLDO:offSDFLDO+4], ld[:])

	rm0, rm1 := sysex.EncodeDB(p.ReplayMode)
	out[offSRPLMD], out[offSRPLMD+1] = rm0, rm1

	se := sysex.EncodeDD(p.End)
	copy(out[offSEND:offSEND+8], se[:])
	ss := sysex.EncodeDD(p.Start)
	copy(out[offSSTART:offSSTART+8], ss[:])
	sloop := sysex.EncodeDD(p.LoopLength)
	copy(out[offSLOOP:offSLOOP+8], sloop[:])

	vc0, vc1 := sysex.EncodeDB(p.VelXFade)
	out[offVC], out[offVC+1] = vc0, vc1
	nr0, nr1 := sysex.EncodeDB(p.Reversed)
	out[offNOREV], out[offNOREV+1] = nr0, nr1

	return out
}

// EncodeHeader writes the 19-byte sample-dump header (including the F0 7E 01
// prefix; F7 is NOT included since the dump SysEx continues into data blocks).
func (h SampleDumpHeader) EncodeHeader() []byte {
	out := make([]byte, 19)
	out[0] = SOX
	out[1] = UniversalNRT
	out[2] = CodeSD
	out[3] = byte(h.Num & 0x7F)
	out[4] = byte((h.Num >> 7) & 0x7F)
	out[5] = h.BitsPerWord & 0x7F

	per := sysex.EncodeTB(h.PeriodNS)
	out[6], out[7], out[8] = per[0], per[1], per[2]
	tot := sysex.EncodeTB(h.TotalWords)
	out[9], out[10], out[11] = tot[0], tot[1], tot[2]
	ls := sysex.EncodeTB(h.LoopStart)
	out[12], out[13], out[14] = ls[0], ls[1], ls[2]
	le := sysex.EncodeTB(h.LoopEnd)
	out[15], out[16], out[17] = le[0], le[1], le[2]
	out[18] = h.Mode & 0x7F
	return out
}

// ParseHeader decodes the 19-byte sample-dump header. The buf passed in must
// include the F0 7E 01 SD prefix; it must NOT be terminated with F7 (the dump
// continues with data blocks before the final F7).
func ParseHeader(buf []byte) (*SampleDumpHeader, error) {
	if len(buf) < 19 {
		return nil, fmt.Errorf("dump header too short: %d", len(buf))
	}
	if buf[0] != SOX || buf[1] != UniversalNRT || buf[2] != CodeSD {
		return nil, errors.New("not a sample dump header")
	}
	h := &SampleDumpHeader{
		Num:         uint16(buf[3]&0x7F) | uint16(buf[4]&0x7F)<<7,
		BitsPerWord: buf[5] & 0x7F,
		PeriodNS:    sysex.DecodeTB([3]byte{buf[6], buf[7], buf[8]}),
		TotalWords:  sysex.DecodeTB([3]byte{buf[9], buf[10], buf[11]}),
		LoopStart:   sysex.DecodeTB([3]byte{buf[12], buf[13], buf[14]}),
		LoopEnd:     sysex.DecodeTB([3]byte{buf[15], buf[16], buf[17]}),
		Mode:        buf[18] & 0x7F,
	}
	return h, nil
}

// EncodeBlock builds one 122-byte sample-data block. blockNum is the 0-based
// index; only the 7-bit LSB is sent on the wire (blocks wrap at 128). words
// must contain exactly WordsPerBlock entries — pad with offset-binary silence
// (0x800) when appending the last block.
func EncodeBlock(blockNum int, words [WordsPerBlock]uint16) []byte {
	out := make([]byte, BlockSize)
	out[0] = byte(blockNum & 0x7F)
	for i := 0; i < WordsPerBlock; i++ {
		b0, b1 := sysex.EncodeSW(words[i])
		out[1+2*i], out[1+2*i+1] = b0, b1
	}
	// Checksum is XOR of the 120 data bytes only (NOT the block-number byte).
	out[BlockSize-1] = sysex.XorChecksum(out[1 : 1+2*WordsPerBlock])
	return out
}

// DecodeBlock parses one 122-byte block and returns the block number (LSB
// only, 0..127) and 60 decoded words. The checksum is validated against the
// XOR of the 120 data bytes.
func DecodeBlock(buf []byte) (blockNumLSB byte, words [WordsPerBlock]uint16, err error) {
	if len(buf) != BlockSize {
		err = fmt.Errorf("block must be %d bytes, got %d", BlockSize, len(buf))
		return
	}
	blockNumLSB = buf[0] & 0x7F
	want := sysex.XorChecksum(buf[1 : 1+2*WordsPerBlock])
	if buf[BlockSize-1] != want {
		err = fmt.Errorf("block %d checksum mismatch: got 0x%02X want 0x%02X",
			blockNumLSB, buf[BlockSize-1], want)
		return
	}
	for i := 0; i < WordsPerBlock; i++ {
		words[i] = sysex.DecodeSW(buf[1+2*i], buf[1+2*i+1])
	}
	return
}

// NumBlocks returns the number of 60-word blocks needed for n total words.
// The last block is zero-padded; the receiver uses the header's TotalWords
// field to recover the true length.
func NumBlocks(n uint32) int {
	return int((n + WordsPerBlock - 1) / WordsPerBlock)
}
