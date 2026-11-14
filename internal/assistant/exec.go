package assistant

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
)

// TokenEnvVar is the environment variable `connect serve` and `connect
// test` read a token from when --token-file is not given. It is removed
// from the environment of every command the executor runs.
const TokenEnvVar = "AGENTDECK_ASSISTANT_TOKEN"

// Executor runs one agent-deck CLI command. args never pass through a shell;
// stdin carries message text so it never appears in argv.
type Executor interface {
	Run(ctx context.Context, profile string, args []string, stdin []byte) ([]byte, error)
}

// ExecError is a command that ran and failed.
type ExecError struct {
	ExitCode int
	Stderr   string
	Stdout   string
}

func (e *ExecError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.Stdout)
	}
	if msg == "" {
		return fmt.Sprintf("agent-deck exited with code %d", e.ExitCode)
	}
	return fmt.Sprintf("agent-deck exited with code %d: %s", e.ExitCode, msg)
}

// BinaryExecutor runs the agent-deck binary at Path (normally
// os.Executable()), adding `-p <profile>` when the token is bound to a
// profile.
type BinaryExecutor struct {
	Path string
	// Timeout bounds one command; 0 means DefaultExecTimeout.
	Timeout time.Duration
}

// DefaultExecTimeout bounds one wrapped command. `session send` waits for
// the agent to be ready, so this is generous.
const DefaultExecTimeout = 3 * time.Minute

// maxCapture bounds what is kept of a command's stdout and stderr.
const maxCapture = 1 << 20

type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
	// cut is set once a byte was dropped.
	cut bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	if len(p) > room {
		c.cut = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

// Run implements Executor.
func (b BinaryExecutor) Run(ctx context.Context, profile string, args []string, stdin []byte) ([]byte, error) {
	if b.Path == "" {
		return nil, errors.New("agent-deck binary path is not set")
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = DefaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	full, err := buildArgv(profile, args)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, b.Path, full...) //nolint:gosec // G204: fixed binary, argv built from validated tool arguments, no shell
	cmd.Env = childenv.ForSelfReexec(TokenEnvVar)
	cmd.Stdin = bytes.NewReader(stdin)
	stdout := &cappedBuffer{limit: maxCapture}
	stderr := &cappedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	if err == nil && stdout.cut {
		// A cut-off prefix (an unterminated JSON array, half an answer) is
		// never returned as a success.
		return nil, fmt.Errorf("agent-deck %s printed more than %d bytes; the output was cut off", firstWord(args), maxCapture)
	}
	if err == nil {
		return stdout.buf.Bytes(), nil
	}
	if ctx.Err() != nil {
		return stdout.buf.Bytes(), fmt.Errorf("agent-deck %s timed out after %s", firstWord(args), timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.buf.Bytes(), &ExecError{
			ExitCode: exitErr.ExitCode(),
			Stderr:   clip(stderr.buf.String(), 2000),
			Stdout:   clip(stdout.buf.String(), 2000),
		}
	}
	return stdout.buf.Bytes(), fmt.Errorf("running agent-deck: %w", err)
}

// maxExecArgs bounds the argv a tool may build. The widest tool call
// (launch_session) uses about ten; the cap keeps the allocation in
// buildArgv bounded whatever a caller passes.
const maxExecArgs = 64

// buildArgv returns the argv for one wrapped command: `-p profile` when a
// profile is set, then args. It refuses more than maxExecArgs arguments, so
// the capacity computed for the allocation can never overflow.
func buildArgv(profile string, args []string) ([]string, error) {
	n := int64(len(args))
	if n > maxExecArgs {
		return nil, fmt.Errorf("too many arguments for agent-deck: %d (max %d)", n, maxExecArgs)
	}
	capacity := int(n) + 2 // n <= maxExecArgs, so this cannot overflow
	full := make([]string, 0, capacity)
	if profile != "" {
		full = append(full, "-p", profile)
	}
	return append(full, args...), nil
}

func firstWord(args []string) string {
	if len(args) == 0 {
		return ""
	}
	if len(args) > 1 && args[0] == "session" {
		return "session " + args[1]
	}
	return args[0]
}

// clip shortens s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
