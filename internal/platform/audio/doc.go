// Package audio is the microphone bridge: it implements internal/audio.Source
// with miniaudio (github.com/gen2brain/malgo) when built with cgo, and returns
// contracts.Unsupported(contracts.CapMicrophone) otherwise.
//
// It only delivers device-native float32 audio; resampling and framing live
// in internal/audio. Device IDs are the hex form of miniaudio's device id
// (a PulseAudio source name, WASAPI endpoint id, CoreAudio UID), stable
// across sessions. Opening "" follows the system default, including
// server-side reroutes when the default changes; opening an explicit ID pins
// the stream to that device, so unplugging it ends the stream with
// ErrDeviceLost (via the stop callback or the pipeline's stall watchdog).
package audio
