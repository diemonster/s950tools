package akaidisk

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestRealImageAudioMatchesOracle is a local-only validation against
// a real library disk (not committed — copyrighted content). Run:
//   REAL_IMG=path/to/disk.img AKAIUTIL=... go test -run TestRealImageAudio
func TestRealImageAudioMatchesOracle(t *testing.T) {
	imgPath := os.Getenv("REAL_IMG")
	if imgPath == "" {
		t.Skip("set REAL_IMG to a real S950 image")
	}
	bin := findAkaiutil(t)
	data, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	im, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var target Entry
	for _, e := range im.Entries() {
		if e.Type == TypeSample {
			target = e // last sample wins; any will do
		}
	}
	if target.Name == "" {
		t.Skip("no samples on image")
	}

	// Ours: read file, parse header, unpack words.
	f, err := im.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	slen := int(f[16]) | int(f[17])<<8 | int(f[18])<<16 | int(f[19])<<24
	words, err := UnpackSamples(f[60:], slen)
	if err != nil {
		t.Fatal(err)
	}

	// Oracle: getwav into a temp dir.
	dir := t.TempDir()
	out := runOracle(t, bin, imgPath, dir, "getwav "+target.Name+".S9")
	wav, err := os.ReadFile(filepath.Join(dir, target.Name+".wav"))
	if err != nil {
		t.Fatalf("oracle export failed (%v):\n%s", err, out)
	}
	di := bytes.Index(wav, []byte("data"))
	if di < 0 {
		t.Fatal("no data chunk")
	}
	pcm := wav[di+8:]
	if len(pcm) < slen*2 {
		t.Fatalf("oracle wav %d bytes, want %d", len(pcm), slen*2)
	}
	for i := 0; i < slen; i++ {
		got := int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8)
		want := int16((int(words[i]) - 2048) << 4)
		if got != want {
			t.Fatalf("sample %d of %q: ours %d vs oracle %d", i, target.Name, want, got)
		}
	}
	t.Logf("verified %d samples of %q byte-exact vs oracle", slen, target.Name)
}

// TestRealImageFullImport pushes EVERY file on a real disk through
// the typed parsers — the exact path the GUI's "Open Gotek image"
// flow uses. Catches real-world program/sample/OVS shapes the
// synthetic round-trip tests can't anticipate.
func TestRealImageFullImport(t *testing.T) {
	imgPath := os.Getenv("REAL_IMG")
	if imgPath == "" {
		t.Skip("set REAL_IMG to a real S950 image")
	}
	data, err := os.ReadFile(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	im, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	var nS, nP, nO, nSkipped int
	for _, e := range im.Entries() {
		f, err := im.ReadFile(e)
		if err != nil {
			t.Fatalf("ReadFile %q: %v", e.Name, err)
		}
		switch e.Type {
		case TypeSample:
			if e.Compressed {
				nSkipped++
				continue
			}
			p, w, err := ParseSampleFile(f)
			if err != nil {
				t.Errorf("sample %q: %v", e.Name, err)
				continue
			}
			if int(p.TotalWords) != len(w) {
				t.Errorf("sample %q: header %d words, unpacked %d", e.Name, p.TotalWords, len(w))
			}
			nS++
		case TypeProgram:
			p, err := ParseProgramFile(f)
			if err != nil {
				t.Errorf("program %q: %v", e.Name, err)
				continue
			}
			if len(p.Keygroups) == 0 {
				t.Errorf("program %q: no keygroups", e.Name)
			}
			nP++
		case TypeOverall:
			if _, err := ParseOverallFile(f); err != nil {
				t.Errorf("OVS: %v", err)
				continue
			}
			nO++
		default:
			nSkipped++ // FIXUPS etc — expected
		}
	}
	t.Logf("imported %d samples, %d programs, %d OVS; skipped %d", nS, nP, nO, nSkipped)
	if nS == 0 || nP == 0 {
		t.Error("expected at least one sample and one program on a library disk")
	}
}
