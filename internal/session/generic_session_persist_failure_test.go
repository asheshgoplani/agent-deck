package session

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// The value of this feature is durability, so "the id is bound" and "the id
// survives a reboot" are different claims. A write-through that fails silently
// leaves them looking identical until the machine restarts and the conversation
// is gone -- at which point there is nothing in the logs and nothing the
// operator can act on.

func TestSetToolSessionID_ReportsAFailedWriteThrough(t *testing.T) {
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), scopeToolConfig)

	storage := newTestStorage(t)
	db := storage.GetDB()
	if db == nil {
		t.Fatal("test storage has no state database")
	}
	prev := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	inst := NewInstance("persist-failure", "/tmp/proj")
	inst.Tool = "mytool"

	// Closing the database is the cheapest honest way to make the targeted
	// write fail the way a full disk or a revoked permission would.
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	_, _, err := SetField(inst, FieldToolSessionID, "sid-never-stored", nil)
	if err == nil {
		t.Fatal("SetField reported success while the binding never reached storage: the operator " +
			"is told the session will resume after a reboot, and it will not")
	}
	if !strings.Contains(err.Error(), "persist") {
		t.Errorf("error %q does not say persistence was the problem", err)
	}
	if inst.GenericSessionPersistError() == nil {
		t.Error("GenericSessionPersistError() is nil after a failed write-through")
	}
}

// TestGenericSessionPersistError_NilWhenDurable keeps the accessor from crying
// wolf on a binding that did reach disk.
func TestGenericSessionPersistError_NilWhenDurable(t *testing.T) {
	_, xdgConfig, _, _ := isolateConfigRoots(t)
	writeConfigAt(t, xdgAgentDeckConfigDir(xdgConfig), scopeToolConfig)

	storage := newTestStorage(t)
	db := storage.GetDB()
	prev := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() { statedb.SetGlobal(prev) })

	inst := NewInstance("persist-ok", "/tmp/proj")
	inst.Tool = "mytool"
	if err := storage.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	if _, _, err := SetField(inst, FieldToolSessionID, "sid-durable", nil); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if err := inst.GenericSessionPersistError(); err != nil {
		t.Errorf("GenericSessionPersistError() = %v after a successful write-through", err)
	}
	if inst.GenericSessionTool != "mytool" || inst.GenericSessionLocation == "" {
		t.Errorf("operator bind recorded no scope: tool=%q location=%q",
			inst.GenericSessionTool, inst.GenericSessionLocation)
	}
}
