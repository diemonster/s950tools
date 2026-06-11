package main

import (
	"fmt"

	"github.com/bivers/s950/internal/device"
	"github.com/bivers/s950/internal/protocol"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// InspectSlicing runs the same pre-flight checks the user sees in the
// modal: validates the slice math against the source, picks free
// slots starting from req.FirstSampleSlot, and reports any
// already-occupied slots that would be overwritten. Returns a
// preflight report even when there are errors (so the UI can surface
// them); the report's OK flag controls whether Continue is enabled.
//
// Holds the App lock for the catalog read so we never race against a
// concurrent connect/disconnect. The actual long-running upload runs
// in ApplySlicing under the same lock.
func (a *App) InspectSlicing(req SlicingRequest) (*SlicingPreflight, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dev == nil {
		return nil, errNotConnected
	}
	return a.inspectSlicingLocked(a.dev, req)
}

// inspectSlicingLocked is the lock-held body. Reused by InspectSlicing
// and ApplySlicing's pre-flight re-check so a single device.Catalog()
// powers both, and both share the same validation rules.
func (a *App) inspectSlicingLocked(d *device.Device, req SlicingRequest) (*SlicingPreflight, error) {
	entries, err := d.Catalog()
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	return planSlicing(entries, req), nil
}

// planSlicing is the pure planner — given a catalog snapshot and a
// SlicingRequest, returns the preflight report (slot picks, occupied
// overwrites, errors, warnings, time estimate). No transport, no
// Wails, no App state, so the full slot-allocation + validation
// matrix is unit-testable. inspectSlicingLocked is the thin shell
// that reads the catalog and hands off here.
func planSlicing(entries []protocol.CatalogEntry, req SlicingRequest) *SlicingPreflight {
	report := &SlicingPreflight{
		SampleSlots: make([]int, 0, len(req.Slices)),
	}

	// Validate slice math up front — same checks BuildSlices runs
	// before any upload, surfaced as user-readable errors here.
	if _, err := device.BuildSlices(req.SourceWords, req.SourceRateHz, req.Slices); err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	if len(req.Slices) > protocol.MaxKeygroups {
		report.Errors = append(report.Errors,
			fmt.Sprintf("too many slices (%d) — max %d keygroups per program",
				len(req.Slices), protocol.MaxKeygroups))
	}
	if int(req.BaseMidiKey)+len(req.Slices)-1 > 127 {
		report.Errors = append(report.Errors,
			fmt.Sprintf("slice mapping runs past MIDI 127 (base=%d, n=%d)",
				req.BaseMidiKey, len(req.Slices)))
	}

	sampleOccupied := map[int]string{}
	programOccupied := map[int]string{}
	for _, e := range entries {
		switch e.Type {
		case 'S':
			sampleOccupied[int(e.Num)] = e.Name
		case 'P':
			programOccupied[int(e.Num)] = e.Name
		}
	}

	// Pick the sample-slot block. A negative FirstSampleSlot is the
	// auto-pick sentinel: we find the lowest run of N consecutive
	// free slots and use it. A non-negative value is a user override
	// (manual mode); overwrites surface as warnings so the user can
	// see what they're about to clobber.
	firstSlot := req.FirstSampleSlot
	if firstSlot < 0 {
		picked, ok := findFreeBlock(sampleOccupied, len(req.Slices))
		if !ok {
			report.Errors = append(report.Errors,
				fmt.Sprintf("no run of %d consecutive free sample slots available", len(req.Slices)))
		}
		firstSlot = picked
	}
	for i := range req.Slices {
		slot := firstSlot + i
		if slot > 99 {
			report.Errors = append(report.Errors,
				fmt.Sprintf("slot %d exceeds S950 maximum (99)", slot))
			break
		}
		report.SampleSlots = append(report.SampleSlots, slot)
		if name, ok := sampleOccupied[slot]; ok && name != "TONE" {
			report.OccupiedSlots = append(report.OccupiedSlots, OccupiedSlot{
				Kind: "sample", Slot: slot, Name: name,
			})
		}
	}

	// Program slot — same auto-pick semantics. A negative req value
	// asks for the lowest free program slot.
	progSlot := req.ProgramSlot
	if progSlot < 0 {
		picked, ok := findFreeProgramSlot(programOccupied)
		if !ok {
			report.Errors = append(report.Errors, "no free program slots available")
		}
		progSlot = picked
	} else if progSlot > 99 {
		report.Errors = append(report.Errors,
			fmt.Sprintf("program slot %d outside 0..99", progSlot))
	}
	report.ProgramSlot = progSlot
	// Same TONE carve-out as the sample-slot warning above: TONE is
	// the S950's boot placeholder and findFreeProgramSlot already
	// treats it as free, so don't contradict that with a stale-name
	// overwrite warning.
	if name, ok := programOccupied[progSlot]; ok && name != "TONE" {
		report.OccupiedSlots = append(report.OccupiedSlots, OccupiedSlot{
			Kind: "program", Slot: progSlot, Name: name,
		})
	}

	// Warnings: occupied slot overwrites are non-blocking but worth
	// surfacing so the user sees what gets replaced.
	for _, oh := range report.OccupiedSlots {
		report.Warnings = append(report.Warnings,
			fmt.Sprintf("%s slot %d (%s) will be overwritten", oh.Kind, oh.Slot, oh.Name))
	}

	// Estimate transfer time using the same drain-time math the
	// transport uses for waits. Each sample-dump block is 122 wire
	// bytes per 60 words; plus ~1.5 KB for the program write.
	var bytes int
	for _, sl := range req.Slices {
		blocks := (int(sl.LengthWords) + 59) / 60
		bytes += blocks * 122
	}
	bytes += 1500
	report.EstimatedSeconds = int(device.ExpectedDrainTime(bytes).Seconds())

	report.OK = len(report.Errors) == 0
	return report
}

// findFreeBlock returns the lowest slot N such that [N..N+count) are
// all unoccupied. The S950's boot-time "TONE" placeholder counts as
// free (it's safe to overwrite; only user-stored samples NAK on
// collision). Returns ok=false if no such block exists in [0..99].
func findFreeBlock(occupied map[int]string, count int) (int, bool) {
	if count <= 0 {
		return 0, false
	}
	for start := 0; start+count <= 100; start++ {
		free := true
		for i := 0; i < count; i++ {
			name, present := occupied[start+i]
			if present && name != "TONE" {
				free = false
				break
			}
		}
		if free {
			return start, true
		}
	}
	return 0, false
}

// findFreeProgramSlot returns the lowest unoccupied program slot.
// Same "TONE" carve-out as sample slots.
func findFreeProgramSlot(occupied map[int]string) (int, bool) {
	for i := 0; i < 100; i++ {
		name, present := occupied[i]
		if !present || name == "TONE" {
			return i, true
		}
	}
	return 0, false
}

// ApplySlicing orchestrates the multi-sample upload + program build.
// Long-running; emits "slicing:progress" events for the transfer
// modal. Holds the App lock for the duration so concurrent JS calls
// can't interleave SysEx on the wire mid-upload.
//
// Re-runs pre-flight before committing — if the device state changed
// between Inspect and Apply (e.g. another tool claimed a slot), we
// fail fast rather than overwrite the wrong slot.
func (a *App) ApplySlicing(req SlicingRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dev == nil {
		return errNotConnected
	}
	d := a.dev

	// Re-run pre-flight: blocks Apply if device state drifted.
	report, err := a.inspectSlicingLocked(d, req)
	if err != nil {
		return err
	}
	if !report.OK {
		// Surface the first error verbatim — the modal already showed
		// the full list during the user's preflight review.
		if len(report.Errors) > 0 {
			return fmt.Errorf("preflight failed: %s", report.Errors[0])
		}
		return fmt.Errorf("preflight failed")
	}

	payloads, err := device.BuildSlices(req.SourceWords, req.SourceRateHz, req.Slices)
	if err != nil {
		a.emitSlicingProgress(SlicingProgress{Phase: "error", Message: err.Error()})
		return err
	}

	// Auto-name slices that didn't carry a name. Matches the
	// "{base}_NN" pattern the UI shows.
	autoNames := device.SliceNames(req.BaseName, len(payloads))
	for i := range payloads {
		if payloads[i].Name == "" {
			payloads[i].Name = autoNames[i]
		}
	}

	total := len(payloads)
	// 90% across sample uploads, 10% for the program write.
	const samplesShare = 90
	const programShare = 10

	// Use the slot list the pre-flight resolved (handles auto-pick).
	// The frontend sends -1 for "auto"; this picks up whatever the
	// catalog scan decided.
	for i, p := range payloads {
		slot := byte(report.SampleSlots[i])
		p.Opts.Num = slot
		a.emitSlicingProgress(SlicingProgress{
			Phase:       "uploading_sample",
			SliceIndex:  i,
			TotalSlices: total,
			Percent:     (i * samplesShare) / total,
			Message:     fmt.Sprintf("Uploading %s → slot %02d", p.Name, slot),
		})
		sent, _, err := d.PutSampleOpenLoop(p.Words, p.Opts)
		if err != nil {
			a.emitSlicingProgress(SlicingProgress{
				Phase: "error", SliceIndex: i, TotalSlices: total,
				Message: fmt.Sprintf("slice %d (%s): %v", i+1, p.Name, err),
			})
			return fmt.Errorf("upload slice %d: %w", i+1, err)
		}
		// Wait for the bytes we just queued to actually reach the
		// wire before sending more. Exact on serial (tcdrain);
		// worst-case-estimate sleep on MIDI, where Send is
		// fire-and-forget into the OS buffer.
		d.WaitTX(sent)

		// Set the SPRM name for the freshly-uploaded slice. The dump
		// itself doesn't carry the name — SPRM does.
		params, err := d.GetParams(slot)
		if err != nil {
			return fmt.Errorf("get SPRM for slot %d: %w", slot, err)
		}
		params.Name = p.Name
		if err := d.SetParams(slot, params); err != nil {
			return fmt.Errorf("set SPRM for slot %d: %w", slot, err)
		}
	}

	// Build + send the program.
	a.emitSlicingProgress(SlicingProgress{
		Phase:       "uploading_program",
		SliceIndex:  -1,
		TotalSlices: total,
		Percent:     samplesShare,
		Message:     fmt.Sprintf("Building program %s with %d keygroups", req.ProgramName, total),
	})
	specs := make([]device.SliceSpec, total)
	for i, p := range payloads {
		specs[i] = device.SliceSpec{Name: p.Name}
	}
	prog, err := device.BuildSliceProgram(req.ProgramName, specs, req.BaseMidiKey)
	if err != nil {
		return fmt.Errorf("build program: %w", err)
	}
	if err := d.SetProgram(byte(report.ProgramSlot), prog); err != nil {
		a.emitSlicingProgress(SlicingProgress{
			Phase: "error", TotalSlices: total,
			Message: fmt.Sprintf("send program: %v", err),
		})
		return fmt.Errorf("send program: %w", err)
	}

	a.emitSlicingProgress(SlicingProgress{
		Phase:       "done",
		SliceIndex:  -1,
		TotalSlices: total,
		Percent:     samplesShare + programShare,
		Message:     fmt.Sprintf("Uploaded %d slices + program %s", total, req.ProgramName),
	})
	return nil
}

// emitSlicingProgress is a thin wrapper around Wails' event emitter
// — checks for a context (startup may not have completed yet during
// tests) and centralises the event name.
func (a *App) emitSlicingProgress(p SlicingProgress) {
	if a.ctx == nil {
		return
	}
	wruntime.EventsEmit(a.ctx, "slicing:progress", p)
}
