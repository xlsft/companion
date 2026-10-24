package protocol

import "fmt"

// Error codes. -32700…-32600 are JSON-RPC 2.0 standard codes; the
// application range is -32000…-32099 (docs/stt-protocol.md#errors).
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603

	CodeModelNotFound      = -32001
	CodeModelChecksum      = -32002
	CodeBackendInitFailed  = -32003
	CodeSessionUnknown     = -32004
	CodeSessionCancelled   = -32005
	CodeNotInitialized     = -32006
	CodeAlreadyInitialized = -32007
	CodeSessionExists      = -32008
	CodeAudioFormat        = -32009
	CodeTranscribeFailed   = -32010
	CodeShuttingDown       = -32011
)

// Error is a JSON-RPC error object. Message is a fixed technical phrase and
// Data a technical detail; neither may ever carry transcript text.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e.Data != "" {
		return fmt.Sprintf("stt: %s (%d): %s", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("stt: %s (%d)", e.Message, e.Code)
}

// Is matches by code so errors decoded off the wire compare equal to the
// sentinels below with errors.Is.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// WithDetail returns a copy carrying a technical detail (path, field name,
// errno), never user content.
func (e *Error) WithDetail(detail string) *Error {
	c := *e
	c.Data = detail
	return &c
}

var (
	ErrParse          = &Error{Code: CodeParseError, Message: "parse error"}
	ErrInvalidRequest = &Error{Code: CodeInvalidRequest, Message: "invalid request"}
	ErrMethodNotFound = &Error{Code: CodeMethodNotFound, Message: "method not found"}
	ErrInvalidParams  = &Error{Code: CodeInvalidParams, Message: "invalid params"}
	ErrInternal       = &Error{Code: CodeInternal, Message: "internal error"}

	ErrModelNotFound      = &Error{Code: CodeModelNotFound, Message: "model not found"}
	ErrModelChecksum      = &Error{Code: CodeModelChecksum, Message: "model checksum mismatch"}
	ErrBackendInitFailed  = &Error{Code: CodeBackendInitFailed, Message: "backend init failed"}
	ErrSessionUnknown     = &Error{Code: CodeSessionUnknown, Message: "session unknown"}
	ErrSessionCancelled   = &Error{Code: CodeSessionCancelled, Message: "session cancelled"}
	ErrNotInitialized     = &Error{Code: CodeNotInitialized, Message: "not initialized"}
	ErrAlreadyInitialized = &Error{Code: CodeAlreadyInitialized, Message: "already initialized"}
	ErrSessionExists      = &Error{Code: CodeSessionExists, Message: "session exists"}
	ErrAudioFormat        = &Error{Code: CodeAudioFormat, Message: "unsupported audio format"}
	ErrTranscribeFailed   = &Error{Code: CodeTranscribeFailed, Message: "transcribe failed"}
	ErrShuttingDown       = &Error{Code: CodeShuttingDown, Message: "shutting down"}
)
