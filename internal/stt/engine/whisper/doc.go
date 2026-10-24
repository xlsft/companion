// Package whisper is the whisper.cpp engine, compiled only with the
// `whisper` build tag and cgo (see docs/stt-protocol.md#whispercpp-build).
// Importing it registers the engine as "whisper". Only cmd/stt-sidecar
// imports it, so the core binary stays pure Go (ADR-0001 rule 6).
package whisper
