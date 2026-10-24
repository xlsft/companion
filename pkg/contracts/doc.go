// Package contracts holds the stable interfaces and value types shared
// between domain packages and per-OS platform bridges (ADR-0001).
//
// Coordinate spaces used throughout:
//
//   - Desktop: logical (DPI-independent) pixels of the virtual desktop. The
//     origin is the top-left of the primary display; other displays may
//     have negative coordinates.
//   - Display-local: logical pixels relative to a display's top-left.
//   - Native: physical pixels of a capture or display buffer.
//   - Normalized capture: [0,1]×[0,1] relative to a capture image, the only
//     space a model is allowed to answer in (FR-OVERLAY-005).
package contracts
