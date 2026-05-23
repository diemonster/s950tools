package main

import (
	"fmt"
	"os"

	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/sysex"
)

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil { panic(err) }
	fmt.Printf("file len = %d\n", len(raw))
	fmt.Printf("first byte = 0x%02X, last byte = 0x%02X\n", raw[0], raw[len(raw)-1])
	hdr, err := protocol.ParseHeader(raw[:19])
	if err != nil { fmt.Println("header:", err); os.Exit(1) }
	fmt.Printf("header: total_words=%d period=%d loop=%d..%d mode=%d\n",
		hdr.TotalWords, hdr.PeriodNS, hdr.LoopStart, hdr.LoopEnd, hdr.Mode)
	body := raw[19 : len(raw)-1]
	fmt.Printf("body len=%d (= %d full blocks + %d extra)\n",
		len(body), len(body)/122, len(body)%122)

	// Check block-num sequence
	nFull := len(body) / 122
	mismatches := 0
	firstMismatch := -1
	for i := 0; i < nFull; i++ {
		expected := byte(i & 0x7F)
		actual := body[i*122]
		if actual != expected {
			if firstMismatch < 0 { firstMismatch = i }
			mismatches++
		}
	}
	fmt.Printf("block-number mismatches: %d/%d (first at block index %d)\n",
		mismatches, nFull, firstMismatch)
	if firstMismatch >= 0 {
		fmt.Printf("around first mismatch:\n")
		start := (firstMismatch-1)*122
		if start < 0 { start = 0 }
		end := (firstMismatch+2) * 122
		if end > len(body) { end = len(body) }
		for i := firstMismatch-1; i <= firstMismatch+1 && i*122 < len(body); i++ {
			if i < 0 { continue }
			off := i*122
			if off+10 > len(body) { break }
			fmt.Printf("  block %d (offset %d): num=0x%02X, first 10 data bytes: % X\n",
				i, off, body[off], body[off+1:off+11])
		}
	}

	// Checksum check
	badCksum := 0
	for i := 0; i < nFull; i++ {
		blk := body[i*122 : (i+1)*122]
		cksum := sysex.XorChecksum(blk[1:121])
		if blk[121] != cksum {
			if badCksum < 3 {
				fmt.Printf("  block %d: chksum got 0x%02X want 0x%02X (num byte 0x%02X)\n",
					i, blk[121], cksum, blk[0])
			}
			badCksum++
		}
	}
	fmt.Printf("bad-checksum blocks: %d\n", badCksum)

	// Last 30 bytes around the trailing extra
	if len(body)%122 != 0 {
		extra := len(body) % 122
		fmt.Printf("trailing extra (%d bytes): % X\n", extra, body[len(body)-extra:])
	}
	fmt.Printf("absolute last 20 bytes of file: % X\n", raw[len(raw)-20:])
}
