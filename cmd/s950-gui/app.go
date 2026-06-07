package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/transport"
	"github.com/bivers/s950/internal/waveformcache"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// hexBytes formats `b` as space-separated uppercase hex pairs — the
// shape the wire-log panel renders ("F0 47 40 ..."). Cap defends
// against giant sample-dump envelopes blowing up the event payload;
// truncated messages get an `…` suffix that the frontend treats
// verbatim.
func hexBytes(b []byte) string {
	const cap = 512
	truncated := false
	if len(b) > cap {
		b = b[:cap]
		truncated = true
	}
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = strings.ToUpper(hex.EncodeToString([]byte{x}))
	}
	out := strings.Join(parts, " ")
	if truncated {
		out += " ..."
	}
	return out
}

// App owns the MIDI transport + device for the lifetime of the
// running Wails app. Frontend calls every exported method through
// the auto-generated wailsjs bindings.
//
// Threading: Wails dispatches JS-originated calls on its own
// goroutine pool; we guard the transport/device with a mutex so
// catalog/get-program/set-program can't interleave on the wire.
type App struct {
	ctx context.Context

	mu      sync.Mutex
	tport   transport.Transport // nil until Connect succeeds
	dev     *device.Device
	channel byte
	// kind tracks which constructor opened the transport: "midi" or
	// "serial". Surfaced via Status so the frontend can gate
	// transport-specific features (Copy-from-S950 only works over
	// serial). Empty when disconnected.
	kind string
	// baud records the serial line rate the transport was opened at,
	// so Status can echo it back to the topbar. Zero for MIDI.
	baud int
	// cache is the lazily-initialised cross-session waveform store
	// (Phase 1B). Behind the same mutex as the transport because
	// wavecache() can be called from multiple Wails-pool goroutines
	// concurrently with Connect/Disconnect — they don't actually
	// share state but pooling the lock keeps the App struct's
	// invariant ("everything mutable goes through mu") simple.
	cache *waveformcache.Cache
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// ---------- Connection management ----------

// ListPorts enumerates MIDI in/out ports and RS-232 serial devices.
// Safe to call before Connect — does not touch the transport. The
// serial list is best-effort: if the OS rejects the query we still
// return MIDI ports rather than failing the whole call.
func (a *App) ListPorts() (*PortList, error) {
	ins, outs, err := transport.ListPorts()
	if err != nil {
		return nil, fmt.Errorf("list MIDI ports: %w", err)
	}
	out := &PortList{
		Ins:    make([]Port, 0, len(ins)),
		Outs:   make([]Port, 0, len(outs)),
		Serial: make([]SerialPort, 0),
	}
	for _, p := range ins {
		out.Ins = append(out.Ins, Port{Name: p.Name})
	}
	for _, p := range outs {
		out.Outs = append(out.Outs, Port{Name: p.Name})
	}
	if ser, serr := transport.ListSerialPorts(); serr == nil {
		for _, p := range ser {
			out.Serial = append(out.Serial, SerialPort{Name: p})
		}
	}
	return out, nil
}

// Connect opens a MIDI transport with the chosen ports + MIDI
// channel and constructs a Device. Closes any prior transport first.
func (a *App) Connect(in, out string, channel int) error {
	if channel < 0 || channel > 15 {
		return fmt.Errorf("channel %d out of range (0..15)", channel)
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	a.closeTransportLocked()

	t, err := transport.Open(transport.Options{
		In:     in,
		Out:    out,
		OnWire: a.makeOnWire(),
	})
	if err != nil {
		return fmt.Errorf("open MIDI transport: %w", err)
	}
	a.adoptTransportLocked(t, "midi", byte(channel), 0)
	return nil
}

// ConnectSerial opens an RS-232 transport. The S950 must already
// have controller-select set to RS-232C on its front panel (M1RS2
// is silently rejected on OVS writes — see project memory). Closes
// any prior transport first.
func (a *App) ConnectSerial(port string, baud int) error {
	if port == "" {
		return fmt.Errorf("serial port name required")
	}
	if baud <= 0 {
		baud = 38400
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	a.closeTransportLocked()

	t, err := transport.OpenSerial(transport.SerialOptions{
		Port:   port,
		Baud:   baud,
		OnWire: a.makeOnWire(),
	})
	if err != nil {
		return fmt.Errorf("open serial transport: %w", err)
	}
	// MIDI channel is moot on RS-232 (point-to-point cable), but the
	// Device wrapper still uses it to address AKAI-exclusive
	// requests. Default to channel 0 — the S950's basic-channel
	// field has omni-on by default, so anything works.
	a.adoptTransportLocked(t, "serial", 0, baud)
	return nil
}

// closeTransportLocked releases any open transport and clears the
// derived state. Caller must hold a.mu.
func (a *App) closeTransportLocked() {
	if a.tport != nil {
		_ = a.tport.Close()
		a.tport = nil
		a.dev = nil
		a.kind = ""
		a.baud = 0
	}
}

// adoptTransportLocked wires a freshly-opened transport into the
// App's state. Caller must hold a.mu.
func (a *App) adoptTransportLocked(t transport.Transport, kind string, channel byte, baud int) {
	a.tport = t
	a.channel = channel
	a.kind = kind
	a.baud = baud
	a.dev = device.New(t, a.channel)
	a.dev.RequestTimeout = 5 * time.Second
}

// makeOnWire returns the wire-log callback the transport publishes
// every SysEx envelope through. Identical for MIDI and serial so
// the frontend's wire-log panel doesn't care which is in use.
func (a *App) makeOnWire() func(direction string, msg []byte) {
	return func(direction string, msg []byte) {
		wruntime.EventsEmit(a.ctx, "wire:traffic", WireMessage{
			Direction: direction,
			Length:    len(msg),
			HexBytes:  hexBytes(msg),
			StampMs:   time.Now().UnixMilli(),
		})
	}
}

// Disconnect closes the active transport, if any. Idempotent.
func (a *App) Disconnect() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tport == nil {
		return nil
	}
	err := a.tport.Close()
	a.tport = nil
	a.dev = nil
	a.kind = ""
	a.baud = 0
	return err
}

// Status reports the current connection state for the topbar.
func (a *App) Status() *ConnectionStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tport == nil {
		return &ConnectionStatus{Connected: false}
	}
	return &ConnectionStatus{
		Connected: true,
		Kind:      a.kind,
		In:        a.tport.InName(),
		Out:       a.tport.OutName(),
		Channel:   int(a.channel),
		Baud:      a.baud,
	}
}

// VerifyDevice pings the connected sampler and reports whether it
// replies. Frontend calls this immediately after Connect/ConnectSerial
// to distinguish "port opened" (the OS-level success the connect
// methods return) from "sampler is actually listening on the wire"
// — the failure mode that prompted the check is RS-232 where the
// serial cable opens fine even if the S950 is still in MIDI
// controller-select mode, leaving the user with a green "connected"
// chip and nothing functional.
//
// Does NOT close the transport on failure — that's the caller's job
// so the UI can drive a clean Disconnect + explanatory modal in one
// place. 2 s matches the SendSample pre-flight ping budget.
func (a *App) VerifyDevice() error {
	d, err := a.lockDevice()
	if err != nil {
		return err
	}
	defer a.mu.Unlock()
	return d.Ping(2 * time.Second)
}

// ---------- Device operations ----------

// Catalog returns programs + samples on the connected device.
func (a *App) Catalog() (*Catalog, error) {
	d, err := a.lockDevice()
	if err != nil {
		return nil, err
	}
	defer a.mu.Unlock()
	entries, err := d.Catalog()
	if err != nil {
		return nil, err
	}
	cat := &Catalog{
		Programs: make([]CatalogItem, 0),
		Samples:  make([]CatalogItem, 0),
	}
	for _, e := range entries {
		item := CatalogItem{Slot: int(e.Num), Name: e.Name}
		switch e.Type {
		case 'P':
			item.Kind = "program"
			cat.Programs = append(cat.Programs, item)
		case 'S':
			item.Kind = "sample"
			cat.Samples = append(cat.Samples, item)
		}
	}
	return cat, nil
}

// GetProgram fetches program N from the device and returns the
// human-editable JSON shape. The hex Raw fields round-trip so unknown
// bytes are preserved on the next SetProgram.
func (a *App) GetProgram(slot int) (*protocol.ProgramJSON, error) {
	if slot < 0 || slot > 99 {
		return nil, fmt.Errorf("slot %d out of range (0..99)", slot)
	}
	d, err := a.lockDevice()
	if err != nil {
		return nil, err
	}
	defer a.mu.Unlock()
	p, err := d.GetProgram(byte(slot))
	if err != nil {
		return nil, err
	}
	j := p.ToJSON()
	return &j, nil
}

// GetSampleParams fetches the 120-byte SPRM block for slot N and
// returns the high-level decoded view. Backs the Sample tab's lazy
// fetch: selecting a sample in the sidebar triggers this to fill in
// the start / end / loop / rate / nominal-pitch fields.
func (a *App) GetSampleParams(slot int) (*protocol.SampleParams, error) {
	if slot < 0 || slot > 99 {
		return nil, fmt.Errorf("slot %d out of range (0..99)", slot)
	}
	d, err := a.lockDevice()
	if err != nil {
		return nil, err
	}
	defer a.mu.Unlock()
	return d.GetParams(byte(slot))
}

// SetSampleParams writes the SPRM block back. The frontend rebuilds
// the SampleParams from its edited Sample type and sends here —
// device.SetParams uses the Raw[] field as the base, then overwrites
// only the high-level fields we modelled, so reserved/undocumented
// bytes from the original read round-trip safely.
func (a *App) SetSampleParams(slot int, p protocol.SampleParams) error {
	if slot < 0 || slot > 99 {
		return fmt.Errorf("slot %d out of range (0..99)", slot)
	}
	d, err := a.lockDevice()
	if err != nil {
		return err
	}
	defer a.mu.Unlock()
	return d.SetParams(byte(slot), &p)
}

// GetOverall reads the device's Overall Settings block. Wrapped
// here as a Wails binding so the frontend can fetch (and later
// edit) device-wide config. Currently no UI consumes this — it
// ships as a CLI-accessible protocol surface and stays available
// for a future Settings tab without another backend round-trip.
func (a *App) GetOverall() (*protocol.OverallSettings, error) {
	d, err := a.lockDevice()
	if err != nil {
		return nil, err
	}
	defer a.mu.Unlock()
	return d.GetOverall()
}

// SetOverall writes Overall Settings back. The frontend is
// responsible for validating ranges (the protocol-layer Encode
// also rejects nonsense, but a UI form should fail fast on bad
// input). M1RS2 writes are silently dropped by the device firmware
// — front panel only for controller-select. See
// project-ovs-write-restriction memory.
func (a *App) SetOverall(o protocol.OverallSettings) error {
	d, err := a.lockDevice()
	if err != nil {
		return err
	}
	defer a.mu.Unlock()
	return d.SetOverall(&o)
}

// GetDrum reads Drum Settings as an opaque round-trip blob. The
// byte layout isn't part of our model, so the returned struct
// carries only the raw 480 bytes; pair with SetDrum for backup +
// restore workflows.
func (a *App) GetDrum() (*protocol.DrumSettings, error) {
	d, err := a.lockDevice()
	if err != nil {
		return nil, err
	}
	defer a.mu.Unlock()
	return d.GetDrum()
}

// SetDrum writes Drum Settings back. Round-trip the bytes from
// GetDrum to restore a previously-saved state.
func (a *App) SetDrum(s protocol.DrumSettings) error {
	d, err := a.lockDevice()
	if err != nil {
		return err
	}
	defer a.mu.Unlock()
	return d.SetDrum(&s)
}

// SetProgram uploads a program (ProgramJSON shape) to slot N. The
// frontend is responsible for running pre-flight checks before
// calling this — the device may NAK on slot collisions or oversize
// keygroup counts.
func (a *App) SetProgram(slot int, j protocol.ProgramJSON) error {
	if slot < 0 || slot > 99 {
		return fmt.Errorf("slot %d out of range (0..99)", slot)
	}
	var p protocol.Program
	if err := p.FromJSON(&j); err != nil {
		return fmt.Errorf("decode ProgramJSON: %w", err)
	}
	d, err := a.lockDevice()
	if err != nil {
		return err
	}
	defer a.mu.Unlock()
	return d.SetProgram(byte(slot), &p)
}

// ---------- Internals ----------

var errNotConnected = errors.New("not connected — pick MIDI in/out and click Connect")

// lockDevice acquires the wire mutex and returns the active device,
// or an error if disconnected. Callers MUST defer a.mu.Unlock() so
// the lock is held for the full duration of the wire conversation
// — Wails dispatches JS-originated calls on its own goroutine pool,
// and any two concurrent device operations would otherwise interleave
// SysEx bytes on the transport. Holding the lock for the whole call
// keeps the request/response cycle atomic on the wire.
func (a *App) lockDevice() (*device.Device, error) {
	a.mu.Lock()
	if a.dev == nil {
		a.mu.Unlock()
		return nil, errNotConnected
	}
	return a.dev, nil
}
