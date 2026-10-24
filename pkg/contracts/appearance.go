package contracts

import "context"

// AppearanceSource reports the system colour scheme and tells when it
// changes, so the UI follows the desktop without a flag (OC-12).
type AppearanceSource interface {
	// Start begins watching until ctx ends. It returns Unsupported
	// (CapColorScheme) when the platform has no way to tell; Dark then
	// stays false and Changes never fires.
	Start(ctx context.Context) error
	// Dark reports whether the system prefers a dark scheme right now.
	Dark() bool
	// Changes receives a value after the scheme changed. Notifications
	// coalesce: a slow reader sees one pending value, then reads Dark.
	Changes() <-chan struct{}
}
