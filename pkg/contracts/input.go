package contracts

import "context"

type MouseButton int

const (
	MouseLeft MouseButton = iota
	MouseRight
	MouseMiddle
)

// InputInjector synthesizes user input for computer use (OC-95…OC-97).
// Positions are desktop coordinates. Implementations must refuse to act
// when ctx is cancelled and must never retry on their own.
type InputInjector interface {
	MoveMouse(ctx context.Context, to Point) error
	Click(ctx context.Context, at Point, button MouseButton, count int) error
	Scroll(ctx context.Context, at Point, dx, dy float64) error
	KeyChord(ctx context.Context, chord Hotkey) error
	TypeText(ctx context.Context, text string) error
}
