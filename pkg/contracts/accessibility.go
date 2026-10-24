package contracts

import "context"

// AppInfo identifies the foreground application for app-specific skills.
type AppInfo struct {
	// BundleID is the most stable id the OS offers: macOS bundle id,
	// Windows executable name, Linux desktop file id or WM_CLASS.
	BundleID string
	Name     string
	PID      int
	// WindowTitle may contain user content; never log it.
	WindowTitle string
}

// ElementRole is a coarse accessibility role.
type ElementRole string

const (
	RoleTextField ElementRole = "text_field"
	RolePassword  ElementRole = "password_field"
	RoleButton    ElementRole = "button"
	RoleOther     ElementRole = "other"
)

// Element is an accessibility node.
type Element struct {
	Role   ElementRole
	Name   string
	Bounds Rect // desktop coordinates
	// Sensitive is true for password, OTP and payment fields; agents and
	// dictation must not fill them (FR-CU-003).
	Sensitive bool
}

// Accessibility exposes the parts of UIA / AX / AT-SPI2 the core needs.
type Accessibility interface {
	ForegroundApp(ctx context.Context) (AppInfo, error)
	FocusedElement(ctx context.Context) (Element, error)
	// InsertText inserts at the caret of the focused element without using
	// the clipboard. Returns ErrUnsupported if the element does not allow it.
	InsertText(ctx context.Context, text string) error
}
