package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
	"github.com/bivers/s950/internal/transport"
)

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
	tport   *transport.Transport // nil until Connect succeeds
	dev     *device.Device
	channel byte
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// ---------- Connection management ----------

// ListPorts enumerates MIDI in/out ports from the rtmidi driver.
// Safe to call before Connect — does not touch the transport.
func (a *App) ListPorts() (*PortList, error) {
	ins, outs, err := transport.ListPorts()
	if err != nil {
		return nil, fmt.Errorf("list MIDI ports: %w", err)
	}
	out := &PortList{
		Ins:  make([]Port, 0, len(ins)),
		Outs: make([]Port, 0, len(outs)),
	}
	for _, p := range ins {
		out.Ins = append(out.Ins, Port{Name: p.Name})
	}
	for _, p := range outs {
		out.Outs = append(out.Outs, Port{Name: p.Name})
	}
	return out, nil
}

// Connect opens a transport with the chosen ports + MIDI channel and
// constructs a Device. Closes any prior transport first so swapping
// ports is one call from the frontend's perspective.
func (a *App) Connect(in, out string, channel int) error {
	if channel < 0 || channel > 15 {
		return fmt.Errorf("channel %d out of range (0..15)", channel)
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.tport != nil {
		_ = a.tport.Close()
		a.tport = nil
		a.dev = nil
	}

	t, err := transport.Open(transport.Options{
		In:  in,
		Out: out,
	})
	if err != nil {
		return fmt.Errorf("open MIDI transport: %w", err)
	}
	a.tport = t
	a.channel = byte(channel)
	a.dev = device.New(t, a.channel)
	a.dev.RequestTimeout = 5 * time.Second
	return nil
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
		In:        a.tport.InName(),
		Out:       a.tport.OutName(),
		Channel:   int(a.channel),
	}
}

// ---------- Device operations ----------

// Catalog returns programs + samples on the connected device.
func (a *App) Catalog() (*Catalog, error) {
	d, err := a.requireDevice()
	if err != nil {
		return nil, err
	}
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
	d, err := a.requireDevice()
	if err != nil {
		return nil, err
	}
	if slot < 0 || slot > 99 {
		return nil, fmt.Errorf("slot %d out of range (0..99)", slot)
	}
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
	d, err := a.requireDevice()
	if err != nil {
		return nil, err
	}
	if slot < 0 || slot > 99 {
		return nil, fmt.Errorf("slot %d out of range (0..99)", slot)
	}
	return d.GetParams(byte(slot))
}

// SetProgram uploads a program (ProgramJSON shape) to slot N. The
// frontend is responsible for running pre-flight checks before
// calling this — the device may NAK on slot collisions or oversize
// keygroup counts.
func (a *App) SetProgram(slot int, j protocol.ProgramJSON) error {
	d, err := a.requireDevice()
	if err != nil {
		return err
	}
	if slot < 0 || slot > 99 {
		return fmt.Errorf("slot %d out of range (0..99)", slot)
	}
	var p protocol.Program
	if err := p.FromJSON(&j); err != nil {
		return fmt.Errorf("decode ProgramJSON: %w", err)
	}
	return d.SetProgram(byte(slot), &p)
}

// ---------- Internals ----------

var errNotConnected = errors.New("not connected — pick MIDI in/out and click Connect")

func (a *App) requireDevice() (*device.Device, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dev == nil {
		return nil, errNotConnected
	}
	return a.dev, nil
}
