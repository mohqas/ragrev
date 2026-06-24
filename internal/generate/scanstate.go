package generate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// scannedPRsFile is an append-only JSONL log of fully-processed PRs.
// Each line records a PR number and the PR's updatedAt at scan time so
// subsequent runs can cheaply skip unchanged PRs (PR-level dedup).
const scannedPRsFile = "scanned_prs.jsonl"

// watermarkFile stores the newest PR updatedAt covered by a completed run.
// It is written only when a run finishes successfully so that interrupted
// runs don't prematurely advance the stop boundary.
const watermarkFile = "scan_state.json"

// scannedPRRecord is a single line in scannedPRsFile.
type scannedPRRecord struct {
	PR        int       `json:"pr"`
	UpdatedAt time.Time `json:"updated_at"`
}

// watermarkState is the content of watermarkFile.
//
// Watermarks are tracked per base branch because `ragrev generate` lists PRs
// filtered by `base`, sorted by updatedAt desc, and stops once it crosses a
// previously-completed scan. A single global watermark would cause a scan of
// base B to stop prematurely against a watermark left by an earlier scan of
// base A, skipping legitimate history on B.
//
// LastUpdated is retained only to read legacy files written before the
// per-branch format. It is not used to gate scans and is not written by new
// runs.
type watermarkState struct {
	LastUpdated time.Time            `json:"last_updated,omitempty"`
	Branches    map[string]time.Time `json:"branches,omitempty"`
}

// formatWatermark renders a watermark for log output, showing "none" for the
// zero value.
func formatWatermark(t time.Time) string {
	if t.IsZero() {
		return "none"
	}
	return t.Format(time.RFC3339)
}

// loadScannedPRs reads the append-only scanned-PR log into a map of
// PR number -> updatedAt. Missing file yields an empty map.
func loadScannedPRs(dbPath string) (map[int]time.Time, error) {
	scanned := make(map[int]time.Time)
	data, err := os.ReadFile(filepath.Join(dbPath, scannedPRsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return scanned, nil
		}
		return scanned, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec scannedPRRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // skip malformed lines, keep going
		}
		scanned[rec.PR] = rec.UpdatedAt
	}
	return scanned, nil
}

// loadWatermark reads the persisted completion watermark for the given base
// branch. Missing file or missing entry yields a zero time (meaning "scan
// everything for this branch"). Legacy single-value files are ignored so a
// previous global watermark cannot truncate a scan of a different branch.
func loadWatermark(dbPath, baseBranch string) time.Time {
	data, err := os.ReadFile(filepath.Join(dbPath, watermarkFile))
	if err != nil {
		return time.Time{}
	}
	var st watermarkState
	if err := json.Unmarshal(data, &st); err != nil {
		return time.Time{}
	}
	if t, ok := st.Branches[baseBranch]; ok {
		return t
	}
	return time.Time{}
}

// saveWatermark persists the completion watermark for the given base branch,
// preserving watermarks for other branches already recorded in the file.
// Called only after a run completes successfully.
func saveWatermark(dbPath, baseBranch string, t time.Time) error {
	if t.IsZero() || baseBranch == "" {
		return nil
	}
	path := filepath.Join(dbPath, watermarkFile)

	// Load existing state (if any) so we don't clobber other branches.
	var st watermarkState
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if st.Branches == nil {
		st.Branches = make(map[string]time.Time)
	}
	st.Branches[baseBranch] = t
	// Drop the legacy field on write so future loads use only the per-branch
	// map.
	st.LastUpdated = time.Time{}

	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// scannedPRWriter appends scanned-PR records incrementally so progress
// survives interruptions on large repos.
type scannedPRWriter struct {
	f *os.File
}

// newScannedPRWriter opens (creating if needed) the scanned-PR log for append.
func newScannedPRWriter(dbPath string) (*scannedPRWriter, error) {
	f, err := os.OpenFile(filepath.Join(dbPath, scannedPRsFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening scanned PRs log: %w", err)
	}
	return &scannedPRWriter{f: f}, nil
}

// record appends a single PR record and flushes it to disk.
func (w *scannedPRWriter) record(pr int, updatedAt time.Time) error {
	data, err := json.Marshal(scannedPRRecord{PR: pr, UpdatedAt: updatedAt})
	if err != nil {
		return err
	}
	if _, err := w.f.Write(append(data, '\n')); err != nil {
		return err
	}
	return w.f.Sync()
}

// Close closes the underlying file.
func (w *scannedPRWriter) Close() error {
	return w.f.Close()
}
