package main

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
