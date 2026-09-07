package clipboard

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/platform"
)

// CopyResult contains metadata about a successful clipboard copy operation.
type CopyResult struct {
	Method    string // How the content was copied (e.g., "pbcopy", "xclip", "osc52")
	ByteSize  int    // Number of bytes copied
	LineCount int    // Number of lines in the content
}

// Copy copies text to the system clipboard using platform-appropriate methods.
// The fallback chain is: native clipboard tool → OSC 52 escape sequence.
// supportsOSC52 should come from tmux.GetTerminalInfo().SupportsOSC52.
func Copy(text string, supportsOSC52 bool) (*CopyResult, error) {
	return CopyContext(context.Background(), text, supportsOSC52)
}

// CopyContext copies text like Copy, but cancels native clipboard subprocesses
// when ctx expires. OSC 52 fallback remains a direct terminal write.
func CopyContext(ctx context.Context, text string, supportsOSC52 bool) (*CopyResult, error) {
	if text == "" {
		return nil, fmt.Errorf("no content to copy")
	}

	lineCount := countLines(text)
	byteSize := len(text)

	// Try native clipboard command first
	method, err := copyNative(ctx, text)
	if err == nil {
		return &CopyResult{
			Method:    method,
			ByteSize:  byteSize,
			LineCount: lineCount,
		}, nil
	}

	// Fall back to OSC 52 if terminal supports it
	if supportsOSC52 {
		if err := copyOSC52(text); err != nil {
			return nil, fmt.Errorf("OSC 52 clipboard failed: %w", err)
		}
		return &CopyResult{
			Method:    "osc52",
			ByteSize:  byteSize,
			LineCount: lineCount,
		}, nil
	}

	return nil, fmt.Errorf("no clipboard method available (install pbcopy, xclip, xsel, or wl-copy)")
}

// copyNative attempts to copy using a platform-native clipboard command.
// Returns the method name on success.
func copyNative(ctx context.Context, text string) (string, error) {
	p := platform.Detect()

	switch p {
	case platform.PlatformMacOS:
		return "pbcopy", runClipCmd(ctx, "pbcopy", nil, text)

	case platform.PlatformWSL1, platform.PlatformWSL2:
		return "clip.exe", runClipCmd(ctx, "clip.exe", nil, text)

	case platform.PlatformLinux:
		// Wayland takes priority over X11
		if os.Getenv("WAYLAND_DISPLAY") != "" {
			if path, err := exec.LookPath("wl-copy"); err == nil {
				return "wl-copy", runClipCmd(ctx, path, nil, text)
			}
		}
		// X11: try xclip first, then xsel
		if path, err := exec.LookPath("xclip"); err == nil {
			return "xclip", runClipCmd(ctx, path, []string{"-selection", "clipboard"}, text)
		}
		if path, err := exec.LookPath("xsel"); err == nil {
			return "xsel", runClipCmd(ctx, path, []string{"--clipboard", "--input"}, text)
		}
		return "", fmt.Errorf("no clipboard command found on Linux")

	default:
		return "", fmt.Errorf("unsupported platform: %s", p)
	}
}

// runClipCmd executes a clipboard command, piping text to its stdin.
func runClipCmd(ctx context.Context, name string, args []string, text string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// copyOSC52 copies text using the OSC 52 terminal escape sequence.
// Inside tmux, wraps the sequence in a DCS passthrough.
func copyOSC52(text string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	seq := generateOSC52(encoded, os.Getenv("TMUX") != "")

	// Write to /dev/tty to bypass any stdout redirection
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("cannot open /dev/tty: %w", err)
	}
	defer tty.Close()

	_, err = tty.WriteString(seq)
	return err
}

// generateOSC52 builds the OSC 52 escape sequence.
// If inTmux is true, wraps it in a DCS passthrough for tmux compatibility.
func generateOSC52(base64Content string, inTmux bool) string {
	osc := "\x1b]52;c;" + base64Content + "\x07"
	if inTmux {
		// tmux DCS passthrough: \ePtmux;\e{OSC}\e\\
		return "\x1bPtmux;\x1b" + osc + "\x1b\\"
	}
	return osc
}

// countLines counts the number of non-empty lines in text.
// A trailing newline does not add an extra line.
func countLines(text string) int {
	if text == "" {
		return 0
	}
	n := strings.Count(text, "\n")
	// If text doesn't end with newline, the last line still counts
	if !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}
