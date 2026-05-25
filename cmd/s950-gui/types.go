package main

import "github.com/bivers/s950/internal/device"

// Wire-friendly types used by Wails JS bindings. We don't expose
// internal/protocol structs directly because some fields (e.g.
// CatalogEntry.Type as byte) marshal awkwardly to JSON. These wrappers
// reshape just enough for clean TypeScript consumption.

// CatalogItem is one program or sample listed by the device.
type CatalogItem struct {
	Kind string `json:"kind"` // "program" | "sample"
	Slot int    `json:"slot"`
	Name string `json:"name"`
}

// Catalog splits the device's catalog into programs and samples so the
// frontend can populate both sidebars without filtering client-side.
type Catalog struct {
	Programs []CatalogItem `json:"programs"`
	Samples  []CatalogItem `json:"samples"`
}

// Port is one MIDI in/out port the user can pick in the topbar.
type Port struct {
	Name string `json:"name"`
}

// PortList is what ListPorts returns — the topbar shows both columns.
type PortList struct {
	Ins  []Port `json:"ins"`
	Outs []Port `json:"outs"`
}

// ConnectionStatus reports whether we currently hold an open MIDI
// transport, and which ports it's wired to.
type ConnectionStatus struct {
	Connected bool   `json:"connected"`
	In        string `json:"in,omitempty"`
	Out       string `json:"out,omitempty"`
	Channel   int    `json:"channel"`
}

// ---------- Slicing ----------

// SlicingRequest is the frontend's full description of a slicing
// operation. SourceWords holds the source sample's 12-bit S950 words
// (the frontend converts the imported .wav before calling). Slot
// allocation is partially controlled by the frontend (FirstSampleSlot
// and ProgramSlot) but InspectSlicing reports what it would actually
// use given the device catalog.
type SlicingRequest struct {
	SourceWords     []uint16            `json:"sourceWords"`
	SourceRateHz    uint32              `json:"sourceRateHz"`
	Slices          []device.SliceSpec  `json:"slices"`
	BaseName        string              `json:"baseName"`
	ProgramName     string              `json:"programName"`
	BaseMidiKey     uint8               `json:"baseMidiKey"`
	FirstSampleSlot int                 `json:"firstSampleSlot"`
	ProgramSlot     int                 `json:"programSlot"`
}

// SlicingPreflight is the report returned by InspectSlicing. Errors
// block Continue in the modal; warnings allow it.
type SlicingPreflight struct {
	OK               bool           `json:"ok"`
	Errors           []string       `json:"errors,omitempty"`
	Warnings         []string       `json:"warnings,omitempty"`
	SampleSlots      []int          `json:"sampleSlots"`     // proposed slots, in order
	ProgramSlot      int            `json:"programSlot"`     // proposed program slot
	EstimatedSeconds int            `json:"estimatedSeconds"`
	OccupiedSlots    []OccupiedSlot `json:"occupiedSlots,omitempty"`
}

// OccupiedSlot describes one already-occupied slot that the slicing
// run would overwrite. Surfaced to the pre-flight modal as a warning
// row so the user sees what they're about to clobber.
type OccupiedSlot struct {
	Kind string `json:"kind"` // "sample" | "program"
	Slot int    `json:"slot"`
	Name string `json:"name"`
}

// SlicingProgress is what we emit on the "slicing:progress" event so
// the transfer modal can show live status.
type SlicingProgress struct {
	Phase       string `json:"phase"`        // "uploading_sample" | "uploading_program" | "done" | "error"
	SliceIndex  int    `json:"sliceIndex"`   // 0-based; -1 for non-sample phases
	TotalSlices int    `json:"totalSlices"`
	Percent     int    `json:"percent"`      // 0..100 over the whole job
	Message     string `json:"message"`
}
