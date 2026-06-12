// "Export Gotek image…" — writes the current programs + samples as
// a bootable S950 floppy image (raw 800 KB .img) for a Gotek/
// FlashFloppy drive. The image carries an OVERALL SE settings file,
// optionally with controller-select forced to RS-232C, so a disk
// auto-loaded at power-on can bring the sampler up RS-232-ready
// (hardware behavior pending the boot test recorded in the project
// notes — the format side is validated against the akaiutil
// reference implementation and a real library disk).

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bivers/s950/internal/akaidisk"
	"github.com/bivers/s950/internal/protocol"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ExportImageSample is one sample to place on the disk: its SPRM
// (wire shape, same as SendSample consumes) plus the 12-bit words.
type ExportImageSample struct {
	Params protocol.SampleParams `json:"params"`
	Words  []uint16              `json:"words"`
}

// ExportImageRequest is the frontend's export payload.
type ExportImageRequest struct {
	Programs []protocol.ProgramJSON `json:"programs"`
	Samples  []ExportImageSample    `json:"samples"`
	// ForceRS232 patches the OVERALL SE file's controller-select to
	// RS-232C (the boot-default payload). Default-on in the UI.
	ForceRS232 bool `json:"forceRS232"`
	// SavePath, when non-empty, writes straight to that file with no
	// dialog — the IMG-editor "Save" (as opposed to "Save as…") for
	// re-saving the image the user opened. Empty keeps the prompt.
	SavePath string `json:"savePath"`
}

// ExportImageResult reports what landed on disk.
type ExportImageResult struct {
	Path       string `json:"path"`
	Files      int    `json:"files"`
	FreeBlocks int    `json:"freeBlocks"` // 1 block = 1 KB
}

// ExportGotekImage prompts for a save path and writes the image.
// Returns nil-nil when the user cancels the dialog (same contract
// as the other save bindings).
func (a *App) ExportGotekImage(req ExportImageRequest) (*ExportImageResult, error) {
	// Overall settings: prefer the connected device's real OVS so
	// unmodelled bytes ride along; fall back to sane defaults when
	// offline. Never let an OVS hiccup block the export.
	ovs := a.exportOVS()

	im, err := buildGotekImage(req, ovs)
	if err != nil {
		return nil, err
	}

	path := req.SavePath
	if path == "" {
		path, err = wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
			Title:           "Save patch image",
			DefaultFilename: "s950-disk.img",
			Filters: []wruntime.FileFilter{
				{DisplayName: "Floppy image (.img)", Pattern: "*.img"},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("save dialog: %w", err)
		}
		if path == "" {
			return nil, nil // user cancelled
		}
	}
	if err := os.WriteFile(path, im.Bytes(), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return &ExportImageResult{
		Path:       path,
		Files:      len(im.Entries()),
		FreeBlocks: im.FreeBlocks(),
	}, nil
}

// exportOVS returns the device's overall settings when a device is
// connected and answering, else defaults tuned for this app's
// workflow (50000 baud — the practical ceiling on firmware 1.2a).
func (a *App) exportOVS() *protocol.OverallSettings {
	a.mu.Lock()
	var o *protocol.OverallSettings
	if a.dev != nil {
		o, _ = a.dev.GetOverall()
	}
	a.mu.Unlock()
	if o != nil {
		return o
	}
	return &protocol.OverallSettings{
		ProgName:        "S950TOOLS ",
		BaudRate:        50000,
		PitchWheelRange: 7,
	}
}

// buildGotekImage is the testable core: assembles the image in
// memory. File order mirrors a sampler-written disk (programs,
// OVERALL SE, then samples). Duplicate sanitized names are an error
// — the S950 addresses files by name and silent shadowing would be
// miserable to debug on hardware.
func buildGotekImage(req ExportImageRequest, ovs *protocol.OverallSettings) (*akaidisk.Image, error) {
	if len(req.Samples) == 0 && len(req.Programs) == 0 {
		return nil, fmt.Errorf("nothing to export — no programs or samples with host audio")
	}

	seen := map[string]string{} // sanitized name → kind, for collision messages
	claim := func(name, kind string) (string, error) {
		s := akaidisk.SanitizeName(name)
		if s == "" {
			return "", fmt.Errorf("%s has an empty name", kind)
		}
		if prev, dup := seen[s]; dup {
			return "", fmt.Errorf("duplicate disk name %q (%s and %s) — the S950 loads files by name; rename one", s, prev, kind)
		}
		seen[s] = kind
		return s, nil
	}

	im := akaidisk.New()

	for i := range req.Programs {
		pj := req.Programs[i]
		seedProgramRaws(&pj)
		var p protocol.Program
		if err := p.FromJSON(&pj); err != nil {
			return nil, fmt.Errorf("program %q: %w", pj.Name, err)
		}
		f, err := akaidisk.BuildProgramFile(&p)
		if err != nil {
			return nil, fmt.Errorf("program %q: %w", pj.Name, err)
		}
		name, err := claim(pj.Name, fmt.Sprintf("program %q", pj.Name))
		if err != nil {
			return nil, err
		}
		if _, err := im.AddFile(name, akaidisk.TypeProgram, f); err != nil {
			return nil, fmt.Errorf("program %q: %w", pj.Name, err)
		}
	}

	ovsFile, err := akaidisk.BuildOverallFile(ovs, req.ForceRS232)
	if err != nil {
		return nil, err
	}
	if _, err := im.AddFile(akaidisk.OverallFileName, akaidisk.TypeOverall, ovsFile); err != nil {
		return nil, fmt.Errorf("overall settings: %w", err)
	}

	for i := range req.Samples {
		s := req.Samples[i]
		if len(s.Words) == 0 {
			return nil, fmt.Errorf("sample %q has no host audio — Copy from S950 or re-import it first", strings.TrimSpace(s.Params.Name))
		}
		// The disk header is the loader's source of truth for
		// length; force consistency rather than trusting two
		// independently-maintained fields to agree.
		s.Params.TotalWords = uint32(len(s.Words))
		f, err := akaidisk.BuildSampleFile(&s.Params, s.Words)
		if err != nil {
			return nil, fmt.Errorf("sample %q: %w", strings.TrimSpace(s.Params.Name), err)
		}
		name, err := claim(s.Params.Name, fmt.Sprintf("sample %q", strings.TrimSpace(s.Params.Name)))
		if err != nil {
			return nil, err
		}
		if _, err := im.AddFile(name, akaidisk.TypeSample, f); err != nil {
			return nil, fmt.Errorf("sample %q: %w", strings.TrimSpace(s.Params.Name), err)
		}
	}

	return im, nil
}

// seedProgramRaws fills empty raw-byte fields with NewDefaultProgram
// seeds so locally-built programs (New Program button, never on the
// device) export cleanly. Device-loaded programs carry their real
// raws and pass through untouched.
func seedProgramRaws(pj *protocol.ProgramJSON) {
	n := len(pj.Keygroups)
	if n == 0 {
		n = 1
	}
	var seed *protocol.ProgramJSON
	getSeed := func() *protocol.ProgramJSON {
		if seed == nil {
			s := protocol.NewDefaultProgram(pj.Name, n).ToJSON()
			seed = &s
		}
		return seed
	}
	if pj.RawHeaderHex == "" {
		pj.RawHeaderHex = getSeed().RawHeaderHex
	}
	for i := range pj.Keygroups {
		if pj.Keygroups[i].RawBytesHex == "" {
			pj.Keygroups[i].RawBytesHex = getSeed().Keygroups[0].RawBytesHex
		}
	}
}
