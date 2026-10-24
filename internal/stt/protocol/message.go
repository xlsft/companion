// Package protocol defines the JSON-RPC 2.0 wire format spoken between the
// core and the STT sidecar over the sidecar's stdin/stdout (ADR-0001, IPC).
// See docs/stt-protocol.md for the method reference.
package protocol

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Version is bumped on any incompatible change to the method set or payloads.
const Version = "1"

const jsonrpcVersion = "2.0"

// Message is one JSON-RPC 2.0 request, notification or response. A single
// struct keeps decoding one-pass: the kind is derived from which fields are
// present.
type Message struct {
	JSONRPC string `json:"jsonrpc"`
	// ID is kept raw so both numeric and string ids round-trip unchanged.
	// Absent means notification; the literal null is only valid in error
	// responses to unparseable requests.
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

func (m *Message) IsRequest() bool      { return m.Method != "" && m.ID != nil }
func (m *Message) IsNotification() bool { return m.Method != "" && m.ID == nil }
func (m *Message) IsResponse() bool     { return m.Method == "" && m.ID != nil }

// DecodeParams unmarshals Params into v, mapping failures to ErrInvalidParams
// so the server can answer with the right code.
func (m *Message) DecodeParams(v any) error {
	if len(m.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(m.Params, v); err != nil {
		return ErrInvalidParams.WithDetail("params")
	}
	return nil
}

// DecodeResult unmarshals Result into v; a nil v discards it.
func (m *Message) DecodeResult(v any) error {
	if v == nil || len(m.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(m.Result, v); err != nil {
		return fmt.Errorf("decode result: %w", err)
	}
	return nil
}

// IntID encodes a numeric request id.
func IntID(id uint64) json.RawMessage { return json.RawMessage(strconv.FormatUint(id, 10)) }

// ParseIntID returns the numeric value of an id produced by IntID.
func ParseIntID(id json.RawMessage) (uint64, bool) {
	v, err := strconv.ParseUint(string(id), 10, 64)
	return v, err == nil
}

// NullID is used for error responses when the request id could not be read.
var NullID = json.RawMessage("null")

func NewRequest(id json.RawMessage, method string, params any) (*Message, error) {
	p, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return &Message{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: p}, nil
}

func NewNotification(method string, params any) (*Message, error) {
	p, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return &Message{JSONRPC: jsonrpcVersion, Method: method, Params: p}, nil
}

func NewResult(id json.RawMessage, result any) (*Message, error) {
	if result == nil {
		result = struct{}{}
	}
	r, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return &Message{JSONRPC: jsonrpcVersion, ID: id, Result: r}, nil
}

func NewErrorResponse(id json.RawMessage, e *Error) *Message {
	if id == nil {
		id = NullID
	}
	return &Message{JSONRPC: jsonrpcVersion, ID: id, Error: e}
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	return json.Marshal(params)
}
