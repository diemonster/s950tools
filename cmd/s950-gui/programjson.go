// Program JSON round-trip via OS file dialogs. The on-disk format
// matches the CLI's put-program / get-program shape so a JSON saved
// here can be uploaded with `s950-tools put-program file.json` (and
// vice-versa). Pure save/load — no embedded sample audio, no
// archive. Samples are still referenced by path the same way the
// CLI's program manifests do, so a fully-portable "patch" would
// need to bundle JSON + WAVs together at the user's discretion.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bivers/s950/internal/protocol"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// SaveProgramJSON prompts the user for a save location and writes
// the given ProgramJSON to disk. Returns the chosen path on
// success, or "" when the user cancelled the dialog.
func (a *App) SaveProgramJSON(j protocol.ProgramJSON, suggestedName string) (string, error) {
	defaultName := suggestedName
	if defaultName == "" {
		defaultName = "program.json"
	}
	if filepath.Ext(defaultName) == "" {
		defaultName += ".json"
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "Save program as JSON",
		DefaultFilename: defaultName,
		Filters: []wruntime.FileFilter{
			{DisplayName: "Program JSON (.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return "", fmt.Errorf("save dialog: %w", err)
	}
	if path == "" {
		return "", nil // user cancelled
	}
	buf, err := json.MarshalIndent(&j, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode JSON: %w", err)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return path, nil
}

// OpenProgramJSON prompts for a JSON file and returns its parsed
// ProgramJSON contents. nil-nil means the user cancelled.
// Compatibility note: this consumes the same shape `put-program`
// reads in the CLI, so files round-trip between the two surfaces.
func (a *App) OpenProgramJSON() (*protocol.ProgramJSON, error) {
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Open program JSON",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Program JSON (.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open dialog: %w", err)
	}
	if path == "" {
		return nil, nil
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	var j protocol.ProgramJSON
	if err := json.Unmarshal(buf, &j); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return &j, nil
}
