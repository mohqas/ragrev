// Package logger provides leveled logging for ragrev CLI output.
//
// Three levels are supported:
//   - Print:   always shown — short, user-facing status messages.
//   - Verbose: shown with -v — per-item progress inside loops, loaded counts, etc.
//   - Debug:   shown with -debug (implies -v) — error responses, retry details.
package logger

import (
	"fmt"
	"io"
)

// Logger writes leveled output to an io.Writer.
type Logger struct {
	w       io.Writer
	verbose bool
	debug   bool
}

// New creates a Logger. When debug is true, verbose is implied.
func New(w io.Writer, verbose, debug bool) *Logger {
	if debug {
		verbose = true
	}
	return &Logger{w: w, verbose: verbose, debug: debug}
}

// Print writes a message that is always shown to the user.
func (l *Logger) Print(format string, args ...any) {
	fmt.Fprintf(l.w, format, args...)
}

// Verbose writes a message only when verbose (or debug) mode is enabled.
func (l *Logger) Verbose(format string, args ...any) {
	if l.verbose {
		fmt.Fprintf(l.w, format, args...)
	}
}

// Debug writes a message only when debug mode is enabled.
func (l *Logger) Debug(format string, args ...any) {
	if l.debug {
		fmt.Fprintf(l.w, format, args...)
	}
}

// Writer returns the underlying io.Writer (useful when a raw writer is still needed).
func (l *Logger) Writer() io.Writer {
	return l.w
}
