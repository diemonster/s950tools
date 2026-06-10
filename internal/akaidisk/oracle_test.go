package akaidisk

// Cross-verification against akaiutil — the de-facto reference
// implementation of the Akai S900/S1000/S3000 filesystems by Klaus
// Michael Indlekofer (https://sourceforge.net/projects/akaiutil/).
// These tests build an image with OUR writer and assert that the
// REFERENCE reader decodes every structure correctly: volume type,
// directory, file headers field-by-field, the forced RS-232
// controller-select in OVERALL SE, and (via WAV export) the 12-bit
// packed audio bytes.
//
// akaiutil is used here strictly as an external test oracle — its
// code is not linked, vendored, or ported (its license permits use
// with credit; see README "References"). The binary is a local,
// optional dev dependency:
//
//	curl -L -o /tmp/akaiutil.tar.gz \
//	  https://sourceforge.net/projects/akaiutil/files/latest/download
//	tar -xzf /tmp/akaiutil.tar.gz -C /tmp && cd /tmp/akaiutil-* && make
//	export AKAIUTIL=/tmp/akaiutil-4.6.8/akaiutil
//
// Tests skip cleanly when the binary is absent ($AKAIUTIL, then
// $PATH), so CI without the oracle still passes — run them locally
// before trusting a format change.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bivers/s950/internal/protocol"
)

func findAkaiutil(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("AKAIUTIL"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		t.Skipf("AKAIUTIL=%q not found", p)
	}
	if p, err := exec.LookPath("akaiutil"); err == nil {
		return p
	}
	t.Skip("akaiutil not installed — see oracle_test.go header for build instructions")
	return ""
}

// runOracle drives akaiutil non-interactively: commands on stdin,
// read-only mount of the image, combined output returned. dir sets
// the working directory (where getwav writes exported files).
func runOracle(t *testing.T, bin, img, dir string, commands ...string) string {
	t.Helper()
	cmd := exec.Command(bin, "-r", "-f", img)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(strings.Join(append(commands, "exit"), "\n") + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("akaiutil failed: %v\n%s", err, out)
	}
	return string(out)
}

// oracleWords is the audio used for packing validation: exercises
// the extremes, the offset-binary midpoint, and an odd count (pad
// path in the packer).
func oracleWords() []uint16 {
	words := make([]uint16, 1001)
	for i := range words {
		words[i] = uint16((i * 7) % 4096)
	}
	words[0] = 0x000
	words[1] = 0xFFF
	words[2] = 0x800
	return words
}

// buildOracleImage writes the test image and returns its path.
func buildOracleImage(t *testing.T) string {
	t.Helper()
	im := New()
	words := oracleWords()
	sp := &protocol.SampleParams{
		Name: "ORACLEKICK", TotalWords: uint32(len(words)), SampleRateHz: 26040,
		NominalPitch: 960, ReplayMode: 'L', End: uint32(len(words) - 1),
		LoopLength: 500, Reversed: 'N',
	}
	sf, err := BuildSampleFile(sp, words)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.AddFile("ORACLEKICK", TypeSample, sf); err != nil {
		t.Fatal(err)
	}
	prog := protocol.NewDefaultProgram("ORACLEPROG", 3)
	pf, err := BuildProgramFile(prog)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.AddFile("ORACLEPROG", TypeProgram, pf); err != nil {
		t.Fatal(err)
	}
	ovs := &protocol.OverallSettings{
		ProgName: "ORACLEVOL ", ControllerSelect: 1, BaudRate: 50000,
		PitchWheelRange: 7,
	}
	of, err := BuildOverallFile(ovs, true) // force RS-232
	if err != nil {
		t.Fatal(err)
	}
	if _, err := im.AddFile(OverallFileName, TypeOverall, of); err != nil {
		t.Fatal(err)
	}

	img := filepath.Join(t.TempDir(), "oracle.img")
	if err := os.WriteFile(img, im.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return img
}

// expectField asserts that `out` contains a "label: value" line,
// whitespace-insensitively.
func expectField(t *testing.T, out, label, value string) {
	t.Helper()
	re := regexp.MustCompile(regexp.QuoteMeta(label) + `:\s+` + regexp.QuoteMeta(value))
	if !re.MatchString(out) {
		t.Errorf("oracle output missing %q: %q\n--- output ---\n%s", label, value, out)
	}
}

func TestOracle_ReferenceImplementationDecodesOurImage(t *testing.T) {
	bin := findAkaiutil(t)
	img := buildOracleImage(t)
	out := runOracle(t, bin, img, t.TempDir(), "infoall")

	// Volume recognized as a low-density S900-family floppy.
	if !strings.Contains(out, "FLL") {
		t.Errorf("volume not recognized as low-density floppy:\n%s", out)
	}

	// Sample header, field by field.
	if !strings.Contains(out, "ORACLEKICK.S9") {
		t.Error("sample file not listed")
	}
	expectField(t, out, "type", "S900 sample")
	expectField(t, out, "ramname", `"ORACLEKICK"`)
	expectField(t, out, "srate", "26040Hz")
	expectField(t, out, "npitch", "60.00")
	expectField(t, out, "pmode", "LOOP")
	expectField(t, out, "dir", "NORM")
	expectField(t, out, "compr", "OFF")
	if !regexp.MustCompile(`scount:\s+0x000003e9`).MatchString(out) {
		t.Errorf("scount (1001 = 0x3e9) not decoded:\n%s", out)
	}

	// Program header.
	if !strings.Contains(out, "ORACLEPROG.P9") {
		t.Error("program file not listed")
	}
	expectField(t, out, "type", "S900 program")
	expectField(t, out, "kgnum", "3")

	// Overall settings — the boot-default payload. ctrlport was
	// forced to RS-232 by BuildOverallFile; the reference decoder
	// must read it back as RS232 with our baud.
	expectField(t, out, "control port", "RS232")
	expectField(t, out, "RS232 baudrate", "50000")
	expectField(t, out, "pitch wheel range", "7")
}

func TestOracle_WavExportMatchesSourceWords(t *testing.T) {
	// The strongest packing check: akaiutil decodes our 12-bit
	// packed data into a WAV; every 16-bit sample must equal our
	// source word's PCM representation ((w - 2048) << 4) exactly —
	// the S900 non-compressed format is lossless in the top 12 bits
	// and zero in the bottom 4.
	bin := findAkaiutil(t)
	img := buildOracleImage(t)
	dir := t.TempDir()
	out := runOracle(t, bin, img, dir, "getwav ORACLEKICK.S9")
	wavPath := filepath.Join(dir, "ORACLEKICK.wav")
	wav, err := os.ReadFile(wavPath)
	if err != nil {
		t.Fatalf("oracle did not export WAV (%v); output:\n%s", err, out)
	}

	// Minimal RIFF parse: find the "data" chunk. (The go-audio/wav
	// decoder is avoided here to keep the comparison byte-exact and
	// independent of its int conversions.)
	di := bytes.Index(wav, []byte("data"))
	if di < 0 {
		t.Fatalf("no data chunk in exported WAV")
	}
	data := wav[di+8:]
	words := oracleWords()
	if len(data) < len(words)*2 {
		t.Fatalf("WAV data %d bytes, want >= %d", len(data), len(words)*2)
	}
	for i, w := range words {
		got := int16(uint16(data[2*i]) | uint16(data[2*i+1])<<8)
		want := int16((int(w) - 2048) << 4)
		if got != want {
			t.Fatalf("sample %d: oracle decoded %d, want %d (word %#03x)", i, got, want, w)
		}
	}
}
