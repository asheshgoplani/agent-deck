package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// These are rendered list frames, rather than assertions on connector flags.
// Each case runs at the three terminal sizes used by tools/visualcheck.
func TestTreeConnectorFrames(t *testing.T) {
	forceTrueColorProfile()
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 40}, {200, 50}} {
		for _, scenario := range []string{"nested", "collapsed-siblings", "remotes"} {
			name := fmt.Sprintf("tree-%s-%dx%d", scenario, size.width, size.height)
			t.Run(name, func(t *testing.T) {
				h := treeConnectorFrameHome(scenario)
				h.width, h.height = size.width, size.height
				frame := h.renderSessionList(size.width, size.height)
				if !strings.Contains(frame, "└─") {
					t.Fatal("fixture did not render a closing connector")
				}
				assertFunctionalGolden(t, name, frame)
			})
		}
	}
}

func treeConnectorFrameHome(scenario string) *Home {
	h := seamBNewHome()
	h.initialLoading = false
	makeSession := func(title, group string, parent *session.Instance) *session.Instance {
		inst := session.NewInstanceWithTool(title, "/tmp/"+title, "claude")
		inst.ID = title
		inst.GroupPath = group
		inst.Status = session.StatusIdle
		if parent != nil {
			inst.SetParent(parent.ID)
		}
		return inst
	}

	var instances []*session.Instance
	switch scenario {
	case "nested":
		root := makeSession("root-first", "alpha", nil)
		last := makeSession("root-last", "alpha", nil)
		child := makeSession("child-last", "alpha", last)
		backend := makeSession("backend-only", "alpha/backend", nil)
		backendChild := makeSession("backend-child", "alpha/backend", backend)
		instances = []*session.Instance{root, last, child, backend, backendChild}
	case "collapsed-siblings":
		parent := makeSession("visible-parent", "alpha", nil)
		live := makeSession("visible-child", "alpha", parent)
		archived := makeSession("archived-child", "alpha", parent)
		archived.ArchivedAt = time.Unix(1, 0)
		collapsed := makeSession("hidden-session", "alpha/hidden", nil)
		visible := makeSession("sibling-session", "alpha/visible", nil)
		instances = []*session.Instance{parent, live, archived, collapsed, visible}
	case "remotes":
		local := makeSession("local-last", "alpha", nil)
		instances = []*session.Instance{local}
		h.remoteSessions = map[string][]session.RemoteSessionInfo{
			"lab": {
				{ID: "remote-root", Title: "remote-root", Tool: "claude", Status: "idle", Group: "work", RemoteName: "lab"},
				{ID: "remote-nested", Title: "remote-nested", Tool: "claude", Status: "idle", Group: "work/api", RemoteName: "lab"},
			},
		}
	}
	h.instances = instances
	h.instanceByID = make(map[string]*session.Instance, len(instances))
	for _, inst := range instances {
		h.instanceByID[inst.ID] = inst
	}
	h.groupTree = session.NewGroupTree(instances)
	if scenario == "collapsed-siblings" {
		h.groupTree.CollapseGroup("alpha/hidden")
	}
	h.refreshSessionRenderSnapshot(instances)
	h.rebuildFlatItems()
	return h
}
