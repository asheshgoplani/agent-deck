package core

import (
	"github.com/asheshgoplani/agent-deck/internal/core/schema"
)

// InputSchema returns the reflected JSON schema of the command's input.
func (d Def) InputSchema() (*schema.Schema, error) {
	return schema.Reflect(d.InType(), SchemaID(d.ID)+"/in", d.ID+" input")
}

// OutputSchema returns the reflected JSON schema of the command's output
// (the envelope's data field).
func (d Def) OutputSchema() (*schema.Schema, error) {
	return schema.Reflect(d.OutType(), SchemaID(d.ID)+"/out", d.ID+" output")
}
