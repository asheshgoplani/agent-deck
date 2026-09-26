package core

import (
	"crypto/rand"
	"encoding/hex"
)

// EnvelopeSchemaVersion is bumped when the envelope shape itself changes.
const EnvelopeSchemaVersion = "v1"

// Envelope is the response shape shared by every surface.
//
// Revision is the store revision the answer was computed at. The store has no
// revision counter yet, so it is always null in slice 1; the field exists so
// clients can rely on the key.
type Envelope struct {
	Schema    string         `json:"schema"`
	RequestID string         `json:"request_id"`
	OK        bool           `json:"ok"`
	Data      any            `json:"data"`
	Warnings  []string       `json:"warnings"`
	Revision  *int64         `json:"revision"`
	Error     *EnvelopeError `json:"error,omitempty"`
}

// EnvelopeError is the error body of a failed envelope.
type EnvelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// SchemaID names the output schema of a command: "agent-deck/<id>/v1".
func SchemaID(id string) string {
	return "agent-deck/" + id + "/" + EnvelopeSchemaVersion
}

// NewRequestID returns a random 16-hex-char request id.
func NewRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// Envelope wraps the result. requestID may be empty, in which case a new id
// is generated.
func (r *Result) Envelope(requestID string) Envelope {
	if requestID == "" {
		requestID = NewRequestID()
	}
	env := Envelope{
		Schema:    SchemaID(r.ID),
		RequestID: requestID,
		Warnings:  append([]string{}, r.Warnings...),
	}
	if r.Err != nil {
		ce := AsError(r.Err)
		env.Error = &EnvelopeError{Code: ce.Code, Message: ce.Message, Data: ce.Data}
		return env
	}
	env.OK = true
	env.Data = r.Out
	return env
}
