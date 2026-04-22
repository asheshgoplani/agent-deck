//go:build !windows
// +build !windows

package tmux

import (
	"context"
	"reflect"
	"testing"
)

// These tests guard the #687 follow-up socket-isolation-at-attach fix
// (v1.7.55). They assert that every command assembly path in pty.go reads
// Session.SocketName via the factory, not via raw exec.Command. A regression
// here is exactly the bug @jcordasco found in v1.7.50: the CLI wrote the
// session's socket name to SQLite, the lifecycle paths honored it on
// start/stop, but Attach / AttachReadOnly / Resize / AttachWindow /
// StreamOutput still built their tmux argv by hand and connected to the
// user's default server — silently defeating the whole feature.

// TestSession_AttachCmd_WithSocket_PrependsDashL: the headline regression.
// Before v1.7.55, pty.go:142 used exec.CommandContext(ctx, "tmux",
// "attach-session", "-t", s.Name) and produced argv[1]="attach-session"
// when a socket was configured. This test fails until the command is built
// via s.tmuxCmdContext, which inserts -L <socket> before the subcommand.
func TestSession_AttachCmd_WithSocket_PrependsDashL(t *testing.T) {
	s := &Session{Name: "agentdeck_iso_abc", SocketName: "agentdeck"}
	cmd := s.attachCmd(context.Background())
	wantArgs := []string{"tmux", "-L", "agentdeck", "attach-session", "-t", s.Name}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("attach must route through tmuxCmdContext so -L lands before subcommand\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}

// TestSession_AttachCmd_EmptySocket_NoDashL: opt-in contract. No config =
// no -L, byte-identical to pre-v1.7.50. A regression here breaks every user
// who has not enabled socket isolation.
func TestSession_AttachCmd_EmptySocket_NoDashL(t *testing.T) {
	s := &Session{Name: "agentdeck_default_abc"}
	cmd := s.attachCmd(context.Background())
	wantArgs := []string{"tmux", "attach-session", "-t", s.Name}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("empty SocketName must produce plain attach argv\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}

// TestSession_AttachReadOnlyCmd_WithSocket_PrependsDashL: read-only attach
// was the SECOND bypass in pty.go (AttachReadOnly, pre-fix line 406). Web
// terminal handler + scripted inspect calls use it — same isolation failure
// mode as interactive attach.
func TestSession_AttachReadOnlyCmd_WithSocket_PrependsDashL(t *testing.T) {
	s := &Session{Name: "agentdeck_ro_abc", SocketName: "agentdeck"}
	cmd := s.attachReadOnlyCmd(context.Background())
	wantArgs := []string{"tmux", "-L", "agentdeck", "attach-session", "-r", "-t", s.Name}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("read-only attach must include -L <socket>\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}

// TestSession_ResizeCmd_WithSocket_PrependsDashL: Resize() called resize-window
// without -L. With isolation on, the user's default server either had no
// such session (silent no-op) or had a stale one (resized the wrong pane).
func TestSession_ResizeCmd_WithSocket_PrependsDashL(t *testing.T) {
	s := &Session{Name: "agentdeck_rsz_abc", SocketName: "agentdeck"}
	cmd := s.resizeCmd(80, 24)
	wantArgs := []string{"tmux", "-L", "agentdeck", "resize-window", "-t", s.Name, "-x", "80", "-y", "24"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("resize must carry -L <socket>\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}

// TestSession_AttachWindowSelectCmd_WithSocket_PrependsDashL: AttachWindow's
// pre-attach select-window step used its own raw exec.Command — so selecting
// window 2 of an isolated session silently targeted a default-server session
// with the same name (or failed).
func TestSession_AttachWindowSelectCmd_WithSocket_PrependsDashL(t *testing.T) {
	s := &Session{Name: "agentdeck_win_abc", SocketName: "agentdeck"}
	cmd := s.selectWindowCmd(2)
	wantArgs := []string{"tmux", "-L", "agentdeck", "select-window", "-t", "agentdeck_win_abc:2"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("select-window must carry -L <socket>\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}

// TestSession_StreamOutputCmd_WithSocket_PrependsDashL: StreamOutput's
// pipe-pane start ran on the wrong server when isolation was on, causing
// the caller to receive no bytes at all.
func TestSession_StreamOutputCmd_WithSocket_PrependsDashL(t *testing.T) {
	s := &Session{Name: "agentdeck_stream_abc", SocketName: "agentdeck"}
	cmd := s.pipePaneStartCmd(context.Background())
	wantArgs := []string{"tmux", "-L", "agentdeck", "pipe-pane", "-t", s.Name, "-o", "cat"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("pipe-pane start must carry -L <socket>\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}

// TestSession_StreamOutputStopCmd_WithSocket_PrependsDashL: and the symmetric
// pipe-pane stop that runs during context cancellation.
func TestSession_StreamOutputStopCmd_WithSocket_PrependsDashL(t *testing.T) {
	s := &Session{Name: "agentdeck_stream_abc", SocketName: "agentdeck"}
	cmd := s.pipePaneStopCmd()
	wantArgs := []string{"tmux", "-L", "agentdeck", "pipe-pane", "-t", s.Name}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("pipe-pane stop must carry -L <socket>\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}
