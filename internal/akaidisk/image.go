// Image reader + writer for low-density S900/S950 floppy images.
// The writer only produces fresh images (sequential allocation from
// block 4) — editing existing images in place is out of scope until
// a concrete need shows up; rebuilding from parsed contents covers
// the same ground without fragmentation handling.

package akaidisk

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Image is a parsed (or under-construction) low-density floppy
// image.
type Image struct {
	data []byte
}

// New returns an empty, formatted low-density image: zeroed
// directory, FAT with only the header blocks implicitly reserved,
// zero volume label (the S900/S950 convention).
func New() *Image {
	return &Image{data: make([]byte, ImageSizeLD)}
}

// Parse validates and wraps an existing image. The buffer is used
// directly (not copied).
func Parse(data []byte) (*Image, error) {
	if len(data) != ImageSizeLD {
		return nil, fmt.Errorf("%w: got %d bytes", ErrBadSize, len(data))
	}
	return &Image{data: data}, nil
}

// Bytes returns the raw image, ready to write to a .img file on the
// Gotek's USB stick.
func (im *Image) Bytes() []byte { return im.data }

// fat returns FAT entry i.
func (im *Image) fat(i int) uint16 {
	return binary.LittleEndian.Uint16(im.data[fatOffset+2*i:])
}

func (im *Image) setFAT(i int, v uint16) {
	binary.LittleEndian.PutUint16(im.data[fatOffset+2*i:], v)
}

// Entries lists the occupied directory slots in directory order.
func (im *Image) Entries() []Entry {
	out := make([]Entry, 0, DirEntries)
	for i := 0; i < DirEntries; i++ {
		e := im.data[i*dirEntrySize : (i+1)*dirEntrySize]
		if e[16] == TypeFree {
			continue
		}
		name := strings.TrimRight(string(e[:NameLen]), " \x00")
		out = append(out, Entry{
			Name:       name,
			Type:       e[16],
			Size:       int(e[17]) | int(e[18])<<8 | int(e[19])<<16,
			StartBlock: int(binary.LittleEndian.Uint16(e[20:22])),
			Compressed: e[16] == TypeSample && binary.LittleEndian.Uint16(e[22:24]) != 0,
		})
	}
	return out
}

// ReadFile returns the file bytes for a directory entry, following
// its FAT chain.
func (im *Image) ReadFile(e Entry) ([]byte, error) {
	out := make([]byte, 0, e.Size)
	blk := e.StartBlock
	for len(out) < e.Size {
		if blk < HeaderBlocks || blk >= TotalBlocksLD {
			return nil, fmt.Errorf("%w: block %d out of range for %q", ErrBadChain, blk, e.Name)
		}
		out = append(out, im.data[blk*BlockSize:(blk+1)*BlockSize]...)
		next := im.fat(blk)
		if next == fatEnd {
			break
		}
		blk = int(next)
		// A chain longer than the disk means a FAT loop.
		if len(out) > ImageSizeLD {
			return nil, fmt.Errorf("%w: FAT loop in %q", ErrBadChain, e.Name)
		}
	}
	if len(out) < e.Size {
		return nil, fmt.Errorf("%w: chain for %q ends before its %d-byte size", ErrBadChain, e.Name, e.Size)
	}
	return out[:e.Size], nil
}

// FreeBlocks reports how many data blocks remain unallocated.
func (im *Image) FreeBlocks() int {
	n := 0
	for i := HeaderBlocks; i < TotalBlocksLD; i++ {
		if im.fat(i) == 0 && !im.blockInUse(i) {
			n++
		}
	}
	return n
}

// blockInUse reports whether block i is the start of (or inside) an
// allocated chain. FAT code 0x0000 is ambiguous on this format —
// it's both "free" and "chain pointer to block 0" never occurs, but
// a single-block file's only marker is its dir entry plus a 0x8000
// FAT code, so a 0 FAT entry alone IS free; this helper exists for
// the writer's sequential allocator, which additionally tracks its
// own high-water mark and never re-reads freed space.
func (im *Image) blockInUse(i int) bool {
	return im.fat(i) != 0
}

// AddFile appends a file to the image: sanitized name, type byte,
// raw bytes. Returns the created entry. Sequential first-fit
// allocation; no fragmentation handling (fresh images only).
func (im *Image) AddFile(name string, ftype byte, data []byte) (Entry, error) {
	name = SanitizeName(name)
	if name == "" {
		return Entry{}, ErrNameEmpty
	}
	// Find a free directory slot.
	slot := -1
	for i := 0; i < DirEntries; i++ {
		if im.data[i*dirEntrySize+16] == TypeFree {
			slot = i
			break
		}
	}
	if slot < 0 {
		return Entry{}, ErrDirFull
	}

	nblocks := (len(data) + BlockSize - 1) / BlockSize
	if nblocks == 0 {
		nblocks = 1 // a zero-byte file still owns one block
	}
	blocks := make([]int, 0, nblocks)
	for i := HeaderBlocks; i < TotalBlocksLD && len(blocks) < nblocks; i++ {
		if im.fat(i) == 0 && !im.chainStart(i) {
			blocks = append(blocks, i)
		}
	}
	if len(blocks) < nblocks {
		return Entry{}, fmt.Errorf("%w: need %d blocks, have %d free", ErrDiskFull, nblocks, len(blocks))
	}

	// Write data + FAT chain.
	for i, blk := range blocks {
		chunk := data[i*BlockSize:]
		if len(chunk) > BlockSize {
			chunk = chunk[:BlockSize]
		}
		copy(im.data[blk*BlockSize:], chunk)
		// Zero the tail of the last block (fresh images are zeroed
		// already; this keeps AddFile correct on reused images).
		if len(chunk) < BlockSize {
			for j := blk*BlockSize + len(chunk); j < (blk+1)*BlockSize; j++ {
				im.data[j] = 0
			}
		}
		if i+1 < len(blocks) {
			im.setFAT(blk, uint16(blocks[i+1]))
		} else {
			im.setFAT(blk, fatEnd)
		}
	}

	// Directory entry: 10-char space-padded name, bytes 10..15 zero
	// (S1000 name extension + tags unused on S900), type, 24-bit
	// size, 16-bit start block, osver zero (non-compressed).
	e := im.data[slot*dirEntrySize : (slot+1)*dirEntrySize]
	for i := range e {
		e[i] = 0
	}
	copy(e, []byte(name+strings.Repeat(" ", NameLen-len(name))))
	e[16] = ftype
	e[17] = byte(len(data))
	e[18] = byte(len(data) >> 8)
	e[19] = byte(len(data) >> 16)
	binary.LittleEndian.PutUint16(e[20:22], uint16(blocks[0]))

	return Entry{Name: name, Type: ftype, Size: len(data), StartBlock: blocks[0]}, nil
}

// chainStart reports whether block i is referenced as the start
// block of any directory entry — needed because a single-block file
// whose FAT code is 0x8000 is detectable, but the allocator must
// also avoid blocks claimed by an entry written moments ago whose
// FAT we've already set. (With fat(i)==0 checked first this only
// guards the pathological case of an entry pointing at a zero-FAT
// block in a hand-built image.)
func (im *Image) chainStart(i int) bool {
	for s := 0; s < DirEntries; s++ {
		e := im.data[s*dirEntrySize : (s+1)*dirEntrySize]
		if e[16] != TypeFree && int(binary.LittleEndian.Uint16(e[20:22])) == i {
			return true
		}
	}
	return false
}
