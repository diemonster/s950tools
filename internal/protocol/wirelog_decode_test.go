package protocol

import (
	"strconv"
	"strings"
	"testing"

	"github.com/bivers/s950/internal/sysex"
)

// Real-hardware header fixtures: the first 56 wire bytes of two PRGM
// replies captured from an S950 (fw 1.2a) over RS-232 on 2026-06-10,
// from the Translator-built "S9x JV Techno" Gotek image. Unlike the
// synthetic fixtures elsewhere, these pin the header offset table
// against bytes a real device emitted — the RESER1 sentinel (always
// 255) landing at offset 44 proves the alignment, and the two
// programs differ in exactly the fields the follow-selection gate
// reads (midiProg / EnableMidiProgram).
func TestProgramHeaderOffsets_RealDeviceCapture(t *testing.T) {
	cases := []struct {
		hexs                 string
		name                 string
		numKgs, midiProg, mp byte
	}{
		{
			hexs: "54 00 4F 00 4E 00 45 00 20 00 50 00 52 00 47 00 52 00 4D 00 " +
				"00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 00 7E 00 44 01 " +
				"00 00 00 00 7F 01 02 00 00 00 00 00 00 00 7F 01",
			name: "TONE PRGRM", numKgs: 2, midiProg: 0, mp: 255,
		},
		{
			hexs: "54 00 45 00 43 00 48 00 4E 00 4F 00 20 00 32 00 20 00 20 00 " +
				"00 00 20 00 20 00 20 00 20 00 20 00 19 00 00 00 18 01 48 01 " +
				"00 00 01 00 7F 01 09 00 00 00 00 00 00 00 00 00",
			name: "TECHNO 2  ", numKgs: 9, midiProg: 0, mp: 0,
		},
	}
	for _, tc := range cases {
		parts := strings.Fields(tc.hexs)
		b := make([]byte, len(parts))
		for i, p := range parts {
			v, err := strconv.ParseUint(p, 16, 8)
			if err != nil {
				t.Fatalf("bad fixture byte %q: %v", p, err)
			}
			b[i] = byte(v)
		}
		name := make([]byte, 10)
		for i := 0; i < 10; i++ {
			name[i] = sysex.DecodeDB(b[pHdrName+i*2], b[pHdrName+i*2+1])
		}
		if got := string(name); got != tc.name {
			t.Errorf("name = %q, want %q", got, tc.name)
		}
		if got := sysex.DecodeDB(b[pHdrReser1], b[pHdrReser1+1]); got != 255 {
			t.Errorf("%s: RESER1 sentinel = %d, want 255 (offset table misaligned)", tc.name, got)
		}
		if got := sysex.DecodeDB(b[pHdrNumKgs], b[pHdrNumKgs+1]); got != tc.numKgs {
			t.Errorf("%s: numKgs = %d, want %d", tc.name, got, tc.numKgs)
		}
		if got := sysex.DecodeDB(b[pHdrMidiPgm], b[pHdrMidiPgm+1]); got != tc.midiProg {
			t.Errorf("%s: midiProg = %d, want %d", tc.name, got, tc.midiProg)
		}
		if got := sysex.DecodeDB(b[pHdrEnableMP], b[pHdrEnableMP+1]); got != tc.mp {
			t.Errorf("%s: EnableMP = %d, want %d", tc.name, got, tc.mp)
		}
	}
}
