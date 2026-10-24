package contracts

import (
	"errors"
	"fmt"
)

var (
	// ErrUnsupported means the current platform or compositor cannot provide
	// the requested capability at all. Callers must switch to the fallback
	// advertised in the CapabilityReport.
	ErrUnsupported = errors.New("unsupported on this platform")
	// ErrPermissionDenied means the capability exists but the user or OS
	// denied it; it may succeed after the user grants permission.
	ErrPermissionDenied = errors.New("permission denied")
	// ErrStale means an operation referenced a capture or display topology
	// that no longer matches the screen (FR-SCREEN-005).
	ErrStale = errors.New("stale screen state")
	// ErrHotkeyConflict means another application already holds the chord;
	// the wrapping error names the binding so the UI can ask for another.
	ErrHotkeyConflict = errors.New("hotkey already taken by another application")
)

// CapabilityError ties a failure to a capability so diagnostics can show it.
type CapabilityError struct {
	Capability Capability
	Err        error
}

func (e *CapabilityError) Error() string { return fmt.Sprintf("%s: %v", e.Capability, e.Err) }
func (e *CapabilityError) Unwrap() error { return e.Err }

func Unsupported(c Capability) error {
	return &CapabilityError{Capability: c, Err: ErrUnsupported}
}

func PermissionDenied(c Capability) error {
	return &CapabilityError{Capability: c, Err: ErrPermissionDenied}
}
