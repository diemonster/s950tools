// Command png2ico converts a square PNG into a multi-resolution
// Windows .ico bundle. Used by scripts/regen-icon.sh to refresh
// cmd/s950-gui/build/windows/icon.ico after the source artwork
// changes — Wails v2 ships .icns (macOS) generation on every
// `wails build` but does not bake the .ico from appicon.png, so
// this tool fills that gap.
//
// Usage:
//   go run ./scripts/png2ico <input.png> <output.ico>
//
// Output format: ICONDIR header + N x ICONDIRENTRY records + N
// PNG-encoded image blobs. Windows Vista+ accepts PNG-embedded
// .ico entries at any size, so we skip the legacy DIB/BMP path —
// every entry is a re-scaled PNG of the source image.

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"os"

	"golang.org/x/image/draw"
)

// Standard Windows icon sizes. 256 is required by Vista+ shell;
// the smaller sizes are still loaded for legacy contexts (taskbar
// tooltips, the Alt-Tab switcher at low DPI, etc).
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	if len(os.Args) != 3 {
		die("usage: png2ico <input.png> <output.ico>")
	}
	src, err := loadPNG(os.Args[1])
	if err != nil {
		die("load %s: %v", os.Args[1], err)
	}

	// Pre-encode every size into a buffer so we can compute the
	// offset table before writing anything.
	encoded := make([][]byte, len(sizes))
	for i, sz := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, scale(src, sz)); err != nil {
			die("encode %dpx: %v", sz, err)
		}
		encoded[i] = buf.Bytes()
	}

	out, err := os.Create(os.Args[2])
	if err != nil {
		die("create %s: %v", os.Args[2], err)
	}
	defer out.Close()

	// ICONDIR — 6 bytes:
	//   uint16 reserved (0)
	//   uint16 type     (1 = .ico, 2 = .cur)
	//   uint16 count    (number of images)
	hdr := new(bytes.Buffer)
	binary.Write(hdr, binary.LittleEndian, uint16(0))
	binary.Write(hdr, binary.LittleEndian, uint16(1))
	binary.Write(hdr, binary.LittleEndian, uint16(len(sizes)))

	// ICONDIRENTRY × N — each 16 bytes, in directory order.
	offset := 6 + 16*len(sizes)
	for i, data := range encoded {
		sz := sizes[i]
		// Width/height encoded as single byte; 0 means 256
		// (legacy hack since the field is only 8 bits wide).
		dim := byte(sz)
		if sz == 256 {
			dim = 0
		}
		hdr.WriteByte(dim)                                       // bWidth
		hdr.WriteByte(dim)                                       // bHeight
		hdr.WriteByte(0)                                         // bColorCount (0 = ≥256 colours)
		hdr.WriteByte(0)                                         // bReserved
		binary.Write(hdr, binary.LittleEndian, uint16(1))        // wPlanes
		binary.Write(hdr, binary.LittleEndian, uint16(32))       // wBitCount
		binary.Write(hdr, binary.LittleEndian, uint32(len(data))) // dwBytesInRes
		binary.Write(hdr, binary.LittleEndian, uint32(offset))   // dwImageOffset
		offset += len(data)
	}

	if _, err := out.Write(hdr.Bytes()); err != nil {
		die("write header: %v", err)
	}
	for _, data := range encoded {
		if _, err := out.Write(data); err != nil {
			die("write data: %v", err)
		}
	}

	fmt.Printf("✓ wrote %s with %d sizes\n", os.Args[2], len(sizes))
}

func loadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// scale resamples src to a square sz×sz RGBA. CatmullRom is sharper
// than BiLinear when downscaling — important for the 16/24/32 sizes
// where soft edges blur small features into mush.
func scale(src image.Image, sz int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, sz, sz))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
