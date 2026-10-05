package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/asheshgoplani/agent-deck/internal/shellwords"
)

// openCodeCLICommand runs the selected launcher, including configured env/args,
// without evaluating a shell string or falling back to a different OpenCode binary.
func (i *Instance) openCodeCLICommand(ctx context.Context, projectPath string, args ...string) (*exec.Cmd, error) {
	words, ok := shellwords.Split(i.openCodeForkBinary())
	if !ok || len(words) == 0 {
		return nil, fmt.Errorf("invalid OpenCode launcher")
	}
	env := os.Environ()
	path := os.Getenv("PATH")
	if dirs := i.spawnPathDirs(); len(dirs) > 0 {
		path = strings.Join(append(dirs, path), string(os.PathListSeparator))
	}
	for len(words) > 0 && isShellEnvAssignment(words[0]) {
		name, value, _ := strings.Cut(words[0], "=")
		value = os.Expand(value, func(key string) string {
			if key == "PATH" {
				return path
			}
			return os.Getenv(key)
		})
		if name == "PATH" {
			path = value
		}
		env = append(env, name+"="+value)
		words = words[1:]
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("OpenCode launcher has no executable")
	}
	binary, found := lookPathIn(words[0], path)
	if !found {
		return nil, fmt.Errorf("OpenCode launcher %q not found", words[0])
	}
	cmd := exec.CommandContext(ctx, binary, append(words[1:], args...)...)
	cmd.Dir = projectPath
	cmd.Env = append(env, "PATH="+path)
	cmd.WaitDelay = 500 * time.Millisecond
	return cmd, nil
}

type openCodeV2Model struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Variant    string `json:"variant,omitempty"`
}

// openCodeV2ModelRef translates the picker/CLI provider/model#variant notation.
func openCodeV2ModelRef(model string) *openCodeV2Model {
	provider, id, ok := strings.Cut(model, "/")
	if !ok || provider == "" || id == "" {
		return nil
	}
	id, variant, _ := strings.Cut(id, "#")
	return &openCodeV2Model{ProviderID: provider, ID: id, Variant: variant}
}

// openCodeV2SessionIDCommand accepts only safe session IDs from an API response.
func openCodeV2SessionIDCommand(variable string) string {
	return fmt.Sprintf(`new_id=$(printf '%%s' "$%s" | tr -d '\n' | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\(ses_[A-Za-z0-9_-]*\)".*/\1/p')`, variable)
}

// openCodeV2OptionCommands applies options to the selected session, never to global config.
func openCodeV2OptionCommands(bin, sessionID string, opts *OpenCodeOptions) []string {
	if opts == nil {
		return nil
	}
	var steps []string
	if opts.Agent != "" {
		body, _ := json.Marshal(map[string]string{"agent": opts.Agent})
		steps = append(steps, fmt.Sprintf("%s api session.switchAgent --param sessionID=%s --data %s || exit 1", bin, sessionID, shellescape.Quote(string(body))))
	}
	if opts.Model != "" {
		model := openCodeV2ModelRef(opts.Model)
		if model == nil {
			return append(steps, `echo 'OpenCode model must use provider/model format' >&2; exit 1`)
		}
		body, _ := json.Marshal(map[string]*openCodeV2Model{"model": model})
		steps = append(steps, fmt.Sprintf("%s api session.switchModel --param sessionID=%s --data %s || exit 1", bin, sessionID, shellescape.Quote(string(body))))
	}
	return steps
}

// buildOpenCodeV2Command preserves model and agent options without v1 TUI flags.
// Fresh sessions with overrides are created through the API, then opened by ID.
func (i *Instance) buildOpenCodeV2Command(command string) string {
	bin := quoteOpenCodeArg(command)
	opts := i.GetOpenCodeOptions()
	if opts == nil {
		cfg, _ := LoadUserConfig()
		opts = NewOpenCodeOptions(cfg)
	}
	sessionID := i.OpenCodeSessionID
	if sessionID == "" && opts.SessionMode == "resume" {
		sessionID = opts.ResumeSessionID
	}
	if sessionID != "" {
		quotedID := shellescape.Quote(sessionID)
		steps := openCodeV2OptionCommands(bin, quotedID, opts)
		steps = append(steps, fmt.Sprintf("%s -s %s", bin, quotedID))
		return strings.Join(steps, " && ")
	}
	if opts.SessionMode == "continue" && opts.Model == "" && opts.Agent == "" {
		// The TUI selects the last session; do not create a replacement for it.
		return bin + " -c"
	}
	if opts.Model == "" && opts.Agent == "" {
		return bin
	}
	body := map[string]any{"title": i.Title, "location": map[string]string{"directory": i.ProjectPath}}
	if opts.Agent != "" {
		body["agent"] = opts.Agent
	}
	if opts.Model != "" {
		model := openCodeV2ModelRef(opts.Model)
		if model == nil {
			return `echo 'OpenCode model must use provider/model format' >&2; exit 1`
		}
		body["model"] = model
	}
	payload, _ := json.Marshal(body)
	var steps []string
	if opts.SessionMode == "continue" {
		steps = append(steps,
			fmt.Sprintf("session_json=$(%s api session.list --param %s --param parentID=null --param limit=1) || exit 1", bin, shellescape.Quote("directory="+i.ProjectPath)),
			openCodeV2SessionIDCommand("session_json"),
			fmt.Sprintf("if [ -z \"$new_id\" ]; then session_json=$(%s api session.create --data %s) || exit 1; %s; fi", bin, shellescape.Quote(string(payload)), openCodeV2SessionIDCommand("session_json")),
		)
		steps = append(steps, openCodeV2OptionCommands(bin, `"$new_id"`, opts)...)
	} else {
		steps = append(steps,
			fmt.Sprintf("session_json=$(%s api session.create --data %s) || exit 1", bin, shellescape.Quote(string(payload))),
			openCodeV2SessionIDCommand("session_json"),
		)
	}
	steps = append(steps,
		`if [ -z "$new_id" ]; then echo 'OpenCode create did not return a session id' >&2; exit 1; fi`,
		`if [ -n "$TMUX" ]; then tmux set-environment OPENCODE_SESSION_ID "$new_id" 2>/dev/null || true; fi`,
		fmt.Sprintf(`exec %s -s "$new_id"`, bin),
	)
	return strings.Join(steps, " && ")
}
