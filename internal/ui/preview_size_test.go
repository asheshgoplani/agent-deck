package ui

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/require"
)

func TestDetachedPreviewFitsAcrossLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	for _, mode := range []string{"initial", "normal", "fresh", "cli", "viewport", "classic", "unselected", "attached", "auxiliary"} {
		t.Run(mode, func(t *testing.T) {
			inst := session.NewInstanceWithTool("preview-"+mode, t.TempDir(), "shell")
			inst.Command = "sleep 300"
			require.NoError(t, inst.Start())
			t.Cleanup(func() { _ = inst.GetTmuxSession().Kill() })
			h := NewHome()
			h.width, h.height, h.embeddedLayout = 142, 51, mode != "classic"
			h.instanceByID[inst.ID] = inst
			h.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
			pane := inst.GetTmuxSession()
			ctl := func(args ...string) string {
				t.Helper()
				if pane.SocketName != "" {
					args = append([]string{"-L", pane.SocketName}, args...)
				}
				out, err := exec.Command("tmux", args...).CombinedOutput()
				require.NoError(t, err, "%s", out)
				return strings.TrimSpace(string(out))
			}
			index, key := -1, inst.ID
			target := pane.Name
			width := func() string { return ctl("display-message", "-p", "-t", target, "#{window_width}") }
			fetch := func() { msg := h.fetchPreview(inst, key, index)().(previewFetchedMsg); require.NoError(t, msg.err) }
			before := width()
			switch mode {
			case "normal", "fresh", "cli":
				fetch()
				switch mode {
				case "normal":
					require.NoError(t, h.restartSession(inst)().(sessionRestartedMsg).err)
				case "fresh":
					require.NoError(t, h.restartSessionFreshWith(inst, nil, func(i *session.Instance) error { return i.RestartFresh() })().(sessionRestartedMsg).err)
				case "cli":
					require.NoError(t, inst.Restart()) // same lifecycle entry as CLI, without Home restart
				}
				pane = inst.GetTmuxSession()
				target = pane.Name
			case "viewport":
				fetch()
				h.width = 162
			case "unselected":
				h.flatItems = nil
			case "attached":
				h.embeddedMode = true
			case "auxiliary":
				ctl("new-window", "-d", "-t", pane.Name+":9", "sleep 300")
				index, key, target = 9, previewCacheKey(inst.ID, 9), pane.Name+":9"
				h.flatItems = []session.Item{{Type: session.ItemTypeWindow, WindowSessionID: inst.ID, WindowIndex: 9}}
			}
			fetch()
			t.Logf("%s: pane width=%s, viewport width=%d", mode, width(), h.embeddedTerminalSize().Cols)
			switch mode {
			case "classic", "unselected", "attached":
				require.Equal(t, before, width())
			default:
				require.Equal(t, strconv.Itoa(h.embeddedTerminalSize().Cols), width())
			}
			if mode == "auxiliary" {
				require.Equal(t, before, ctl("display-message", "-p", "-t", pane.Name+":^", "#{window_width}"))
			}
		})
	}
}
