package contracts

// SessionID identifies one user interaction (voice/text Talk, dictation,
// agent run). Generation increases on every restart of the same session so
// late events from a cancelled generation can be dropped (FR-VOICE-008).
type SessionID string

// SessionRef pins an event to a specific generation of a session.
type SessionRef struct {
	ID         SessionID
	Generation uint64
}
