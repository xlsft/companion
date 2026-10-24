// Package tts defines the text-to-speech provider contract (PRD §9.6) and
// the shared streaming/HTTP machinery used by concrete providers.
//
// Streaming model: SynthesizeStream returns a Stream whose Next blocks until
// the next audio chunk arrives, returns io.EOF after the last one, and any
// other error (never a silent EOF) if the connection breaks. A stream is a
// pull API rather than a channel so errors and EOF are unambiguous and the
// consumer controls backpressure. Hard cancel is available three ways, all of
// which abort the underlying HTTP request promptly: cancelling the ctx given
// to SynthesizeStream, calling Stream.Close, or calling Provider.Cancel.
//
// Secrets: errors and logs from this package never contain the API key or the
// synthesized text; HTTP error bodies are deliberately not read into errors.
package tts
