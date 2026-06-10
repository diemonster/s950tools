package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Pure-function tests for GUI-side helpers that don't touch the
// transport, the wails runtime, or the App lock. These live in
// their own _test.go (separate from the existing fakePingTransport
// integration tests) so the table-driven cases stay readable.

func TestHexBytes_FormatsUppercaseSpaceSeparated(t *testing.T) {
	got := hexBytes([]byte{0xF0, 0x47, 0x00, 0xF7})
	want := "F0 47 00 F7"
	if got != want {
		t.Errorf("hexBytes = %q, want %q", got, want)
	}
}

func TestHexBytes_EmptyReturnsEmpty(t *testing.T) {
	if got := hexBytes(nil); got != "" {
		t.Errorf("hexBytes(nil) = %q, want empty", got)
	}
	if got := hexBytes([]byte{}); got != "" {
		t.Errorf("hexBytes([]) = %q, want empty", got)
	}
}

func TestHexBytes_TruncatesPastCap(t *testing.T) {
	// 600 bytes > the 512-byte cap. The hex output must end with " ..."
	// and contain exactly 512 hex tokens before that.
	in := make([]byte, 600)
	got := hexBytes(in)
	if !strings.HasSuffix(got, " ...") {
		t.Errorf("expected truncated suffix, got tail %q", tail(got))
	}
	body := strings.TrimSuffix(got, " ...")
	tokens := strings.Split(body, " ")
	if len(tokens) != 512 {
		t.Errorf("expected 512 hex tokens before truncation marker, got %d", len(tokens))
	}
}

func TestHexBytes_AtCapNoTruncationMarker(t *testing.T) {
	// Exactly the cap — no marker.
	in := make([]byte, 512)
	got := hexBytes(in)
	if strings.Contains(got, "...") {
		t.Errorf("512-byte input should not be truncated, got tail %q", tail(got))
	}
}

func tail(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[len(s)-16:]
}

func TestSuggestName_StripsExtensionUppercasesTruncatesToTen(t *testing.T) {
	cases := []struct {
		path, want string
	}{
		{"/foo/bar.wav", "BAR"},
		{"kick.aiff", "KICK"},
		{"snare-808-loop.wav", "SNARE-808-"},  // truncated to 10
		{"a.b.c.wav", "A.B.C"},                // only last dot stripped
		{"/abs/path/PERCUSSION_GROUP.wav", "PERCUSSION"},
		{"plain", "PLAIN"},                    // no extension is fine
		{"weirdéname.wav", "WEIRDNAME"},  // non-ASCII dropped
	}
	for _, c := range cases {
		if got := suggestName(c.path); got != c.want {
			t.Errorf("suggestName(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestSuggestName_LeadingDotIsNotStripped(t *testing.T) {
	// `.bashrc`-style names: LastIndex returns 0; the guard `dot > 0`
	// is what keeps these intact. Pin the behaviour.
	if got := suggestName(".hidden"); got != ".HIDDEN" {
		t.Errorf("suggestName(.hidden) = %q, want %q", got, ".HIDDEN")
	}
}

func TestFilterUSBSerialPorts_KeepsLikelyEntries(t *testing.T) {
	// Mix likely + clearly-not-likely names; the filter should
	// return only the platform-appropriate likely ones. We seed
	// every OS's expected shape so the assertion can pick the
	// right one for whichever host the test runs on.
	in := []string{
		"/dev/cu.usbserial-AB12",       // darwin yes
		"/dev/cu.Bluetooth-Incoming",   // darwin no
		"/dev/cu.usbmodem1101",         // darwin no (CDC-ACM dev board)
		"/dev/ttyUSB0",                 // linux yes
		"/dev/ttyACM0",                 // linux no
		"COM3",                         // windows yes
		"COM7",                         // windows yes
		"random-string",
	}
	got := filterUSBSerialPorts(in)
	for _, p := range got {
		if !isLikelyUSBSerial(p) {
			t.Errorf("filterUSBSerialPorts returned %q but isLikelyUSBSerial says no", p)
		}
	}
	// Inverse: every dropped entry must have been a "no".
	for _, p := range in {
		dropped := true
		for _, k := range got {
			if k == p {
				dropped = false
				break
			}
		}
		if dropped && isLikelyUSBSerial(p) {
			t.Errorf("filter dropped %q but isLikelyUSBSerial says yes", p)
		}
	}
}

func TestIsLikelyUSBSerial_Darwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only filter behaviour")
	}
	yes := []string{"/dev/cu.usbserial-AB12", "/dev/cu.usbserial-FOO"}
	no := []string{
		"/dev/cu.Bluetooth-Incoming-Port",
		"/dev/cu.usbmodem1234",
		"/dev/tty.usbserial-AB12", // tty.* duplicate of cu.* must be dropped
		"",
	}
	for _, n := range yes {
		if !isLikelyUSBSerial(n) {
			t.Errorf("isLikelyUSBSerial(%q) = false, want true", n)
		}
	}
	for _, n := range no {
		if isLikelyUSBSerial(n) {
			t.Errorf("isLikelyUSBSerial(%q) = true, want false", n)
		}
	}
}

func TestIsLikelyUSBSerial_Linux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux-only filter behaviour")
	}
	if !isLikelyUSBSerial("/dev/ttyUSB0") {
		t.Error("/dev/ttyUSB0 should be likely-USB-serial on linux")
	}
	if isLikelyUSBSerial("/dev/ttyACM0") {
		t.Error("/dev/ttyACM0 should NOT be likely-USB-serial on linux (CDC-ACM dev boards)")
	}
}

func TestIsLikelyUSBSerial_Windows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only filter behaviour")
	}
	for _, n := range []string{"COM1", "COM10", "com3"} {
		if !isLikelyUSBSerial(n) {
			t.Errorf("isLikelyUSBSerial(%q) = false, want true", n)
		}
	}
	if isLikelyUSBSerial("USB") {
		t.Error("plain 'USB' should not match on windows")
	}
}

func TestFindFreeBlock_ReturnsLowestRun(t *testing.T) {
	// 0..3 occupied, 4..9 free, 10 occupied → lowest 5-slot block is [4..8].
	occ := map[int]string{
		0: "KICK", 1: "SNARE", 2: "HAT", 3: "RIDE",
		10: "BASS",
	}
	start, ok := findFreeBlock(occ, 5)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if start != 4 {
		t.Errorf("start = %d, want 4", start)
	}
}

func TestFindFreeBlock_TONEPlaceholderCountsAsFree(t *testing.T) {
	// Boot-time TONE entries are safe to overwrite — must not block a run.
	occ := map[int]string{
		0: "TONE", 1: "TONE", 2: "TONE",
		3: "REAL", // breaks the run at 3
	}
	start, ok := findFreeBlock(occ, 3)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if start != 0 {
		t.Errorf("start = %d, want 0 (TONE should count as free)", start)
	}
}

func TestFindFreeBlock_NoRoomReturnsNotOk(t *testing.T) {
	occ := map[int]string{}
	// Every slot in 0..99 occupied by real samples — no run of 2 free.
	for i := 0; i < 100; i++ {
		occ[i] = "FULL"
	}
	if _, ok := findFreeBlock(occ, 2); ok {
		t.Error("expected ok=false when fully occupied")
	}
}

func TestFindFreeBlock_RejectsZeroOrNegativeCount(t *testing.T) {
	occ := map[int]string{}
	if _, ok := findFreeBlock(occ, 0); ok {
		t.Error("count=0 must return ok=false")
	}
	if _, ok := findFreeBlock(occ, -1); ok {
		t.Error("count=-1 must return ok=false")
	}
}

func TestFindFreeBlock_RunAtEndStillFits(t *testing.T) {
	// Slots 0..94 occupied, 95..99 free → count=5 must succeed at 95.
	occ := map[int]string{}
	for i := 0; i < 95; i++ {
		occ[i] = "X"
	}
	start, ok := findFreeBlock(occ, 5)
	if !ok || start != 95 {
		t.Errorf("findFreeBlock(95..99 free, 5) = (%d, %v), want (95, true)", start, ok)
	}
	// count=6 must NOT succeed: only 5 free slots remain.
	if _, ok := findFreeBlock(occ, 6); ok {
		t.Error("count=6 across the boundary should return ok=false")
	}
}

func TestFindFreeProgramSlot_ReturnsLowestFree(t *testing.T) {
	occ := map[int]string{0: "P1", 1: "P2"}
	slot, ok := findFreeProgramSlot(occ)
	if !ok || slot != 2 {
		t.Errorf("findFreeProgramSlot = (%d, %v), want (2, true)", slot, ok)
	}
}

func TestFindFreeProgramSlot_TONECountsAsFree(t *testing.T) {
	occ := map[int]string{0: "TONE", 1: "REAL"}
	slot, ok := findFreeProgramSlot(occ)
	if !ok || slot != 0 {
		t.Errorf("findFreeProgramSlot = (%d, %v), want (0, true)", slot, ok)
	}
}

func TestFindFreeProgramSlot_FullCatalog(t *testing.T) {
	occ := map[int]string{}
	for i := 0; i < 100; i++ {
		occ[i] = "P"
	}
	if _, ok := findFreeProgramSlot(occ); ok {
		t.Error("100/100 occupied should return ok=false")
	}
}

func TestPlural(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "s"},
		{1, ""},
		{2, "s"},
		{-1, "s"}, // unusual but pin behaviour
	}
	for _, c := range cases {
		if got := plural(c.n); got != c.want {
			t.Errorf("plural(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestNoteOnBytes_StatusAndPayload(t *testing.T) {
	// Note On status nibble is 0x9; channel goes in the low 4 bits.
	got := noteOnBytes(60, 127, 3)
	want := []byte{0x93, 60, 127}
	if !bytesEqual(got, want) {
		t.Errorf("noteOnBytes(60,127,3) = % X, want % X", got, want)
	}
}

func TestNoteOffBytes_StatusAndZeroVelocity(t *testing.T) {
	// Note Off uses status 0x8 and forces velocity to 0 (avoid
	// release-velocity contamination — the GUI's preview is "stop").
	got := noteOffBytes(72, 0)
	want := []byte{0x80, 72, 0}
	if !bytesEqual(got, want) {
		t.Errorf("noteOffBytes(72,0) = % X, want % X", got, want)
	}
}

func TestNoteOnBytes_ClampsOutOfRangeArgs(t *testing.T) {
	// Channel > 15 wraps via & 0x0F; note > 127 / velocity > 127 wrap
	// via & 0x7F. Pin the wrap so an out-of-range frontend value
	// doesn't corrupt the status byte by overflowing into the high
	// nibble.
	got := noteOnBytes(0xFF, 0xFF, 0xFF)
	if got[0]&0xF0 != 0x90 {
		t.Errorf("status nibble corrupted by channel overflow: %#x", got[0])
	}
	if got[1] > 0x7F {
		t.Errorf("note byte not 7-bit clamped: %#x", got[1])
	}
	if got[2] > 0x7F {
		t.Errorf("velocity byte not 7-bit clamped: %#x", got[2])
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCacheDir_ContainsExpectedSubdir(t *testing.T) {
	// cacheDir only fails when os.UserCacheDir does — on every
	// supported platform the OS-standard cache root exists in CI.
	dir, err := cacheDir()
	if err != nil {
		t.Skipf("user cache dir unavailable on this host: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(dir), "/"+wavecacheSubdir) {
		t.Errorf("cacheDir() = %q, expected to end in %q", dir, wavecacheSubdir)
	}
}
