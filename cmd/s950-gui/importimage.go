// "Open Gotek image…" — loads a floppy image's programs + samples
// into the app. Together with ExportGotekImage this makes the .img
// the app's patch format: one file that round-trips entire kits
// through the app AND boots the real sampler.

package main

import (
	"fmt"
	"os"

	"github.com/bivers/s950/internal/akaidisk"
	"github.com/bivers/s950/internal/protocol"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ImportedSample is one sample recovered from an image: SPRM (wire
// shape, same as the rest of the bindings) plus the 12-bit words.
type ImportedSample struct {
	Params protocol.SampleParams `json:"params"`
	Words  []uint16              `json:"words"`
}

// ImportImageResult is OpenGotekImage's payload. Skipped lists
// files we deliberately didn't import (OS fixups, compressed
// samples, unknown types) with human-readable reasons.
type ImportImageResult struct {
	Path     string                 `json:"path"`
	Samples  []ImportedSample       `json:"samples"`
	Programs []protocol.ProgramJSON `json:"programs"`
	Skipped  []string               `json:"skipped"`
}

// OpenGotekImage prompts for an image file and returns its parsed
// contents for the frontend to load into the stores. nil-nil on
// dialog cancel.
func (a *App) OpenGotekImage() (*ImportImageResult, error) {
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Open Gotek floppy image",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Floppy image (.img)", Pattern: "*.img"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open dialog: %w", err)
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	res, err := importGotekImage(data)
	if err != nil {
		return nil, err
	}
	res.Path = path
	return res, nil
}

// importGotekImage is the testable core: image bytes in, typed
// contents out. Per-file parse failures are downgraded to Skipped
// entries — a half-readable disk should still yield everything it
// can (matching how the S950 itself tolerates odd files), with the
// failures visible rather than silent.
func importGotekImage(data []byte) (*ImportImageResult, error) {
	im, err := akaidisk.Parse(data)
	if err != nil {
		return nil, err
	}
	res := &ImportImageResult{}
	for _, e := range im.Entries() {
		f, err := im.ReadFile(e)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (unreadable: %v)", e.Name, err))
			continue
		}
		switch e.Type {
		case akaidisk.TypeSample:
			if e.Compressed {
				res.Skipped = append(res.Skipped,
					fmt.Sprintf("%s (compressed sample format not supported yet)", e.Name))
				continue
			}
			p, words, err := akaidisk.ParseSampleFile(f)
			if err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%v)", e.Name, err))
				continue
			}
			res.Samples = append(res.Samples, ImportedSample{Params: *p, Words: words})
		case akaidisk.TypeProgram:
			p, err := akaidisk.ParseProgramFile(f)
			if err != nil {
				res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%v)", e.Name, err))
				continue
			}
			res.Programs = append(res.Programs, p.ToJSON())
		case akaidisk.TypeOverall:
			// Settings travel on exported disks for the sampler's
			// benefit (boot RS-232); there's nothing to load them
			// into app-side. Not worth a skipped line.
		default:
			res.Skipped = append(res.Skipped,
				fmt.Sprintf("%s (type %c not imported)", e.Name, e.Type))
		}
	}
	if len(res.Samples) == 0 && len(res.Programs) == 0 {
		return nil, fmt.Errorf("no importable programs or samples on this image (%d file(s) skipped)", len(res.Skipped))
	}
	return res, nil
}
