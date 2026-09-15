package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// RemoteCreationCatalog is an owner-host snapshot of the actual creation
// parsers and selectable definitions. Config directories and credentials are
// deliberately excluded. Unknown protocol versions fail closed.
type RemoteCreationCatalog struct {
	// Legacy is controller-only state, never part of the catalog protocol.
	Legacy      bool                             `json:"-"`
	Defaults    map[string]bool                  `json:"defaults"`
	Version     int                              `json:"version"`
	Commands    map[string][]RemoteCreationField `json:"commands"`
	Tools       []RemoteCreationTool             `json:"tools"`
	DefaultTool string                           `json:"default_tool"`
	Accounts    []string                         `json:"accounts"`
	MCPs        []string                         `json:"mcps"`
	Conductors  []RemoteCreationConductor        `json:"conductors"`
}

type RemoteCreationField struct {
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases,omitempty"`
	TakesValue bool     `json:"takes_value"`
}

type RemoteCreationTool struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"default_model"`
}

type RemoteCreationConductor struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (r *SSHRunner) FetchCreationCatalog(ctx context.Context) (*RemoteCreationCatalog, error) {
	data, err := r.Run(ctx, "add", "--capabilities", "--json")
	if err != nil {
		sessionLog.Warn("remote creation catalog request failed", "error", err)
		// Only the old parser's rejection of this exact probe permits fallback.
		if strings.Contains(err.Error(), "exit status 2: flag provided but not defined: -capabilities\n") || strings.HasSuffix(err.Error(), "exit status 2: flag provided but not defined: -capabilities") {
			return LegacyRemoteCreationCatalog(), nil
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("remote creation catalog request timed out")
		}
		return nil, fmt.Errorf("remote creation catalog unavailable; check the SSH connection")
	}
	var catalog RemoteCreationCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		sessionLog.Warn("invalid remote creation catalog", "error", err, "output", string(data))
		return nil, fmt.Errorf("invalid remote creation catalog; update the remote")
	}
	if catalog.Version != 1 || len(catalog.Commands) == 0 {
		sessionLog.Warn("unsupported remote creation catalog", "output", string(data))
		return nil, fmt.Errorf("unsupported remote creation catalog version %d; update the remote", catalog.Version)
	}
	return &catalog, nil
}

// LegacyRemoteCreationCatalog describes the pre-catalog creation contract.
// It deliberately excludes options whose support cannot be established.
func LegacyRemoteCreationCatalog() *RemoteCreationCatalog {
	fields := []RemoteCreationField{
		{Name: "title", Aliases: []string{"t"}, TakesValue: true},
		{Name: "group", Aliases: []string{"g"}, TakesValue: true},
		{Name: "cmd", Aliases: []string{"c"}, TakesValue: true},
		{Name: "worktree", Aliases: []string{"w"}, TakesValue: true},
		{Name: "new-branch", Aliases: []string{"b"}},
		{Name: "location", TakesValue: true},
		{Name: "sandbox"}, {Name: "sandbox-image", TakesValue: true},
		{Name: "additional-path", TakesValue: true},
		{Name: "json"}, {Name: "quiet", Aliases: []string{"q"}},
	}
	add := append(append([]RemoteCreationField(nil), fields...), RemoteCreationField{Name: "quick", Aliases: []string{"Q"}}, RemoteCreationField{Name: "create-dir"})
	launch := append(append([]RemoteCreationField(nil), fields...),
		RemoteCreationField{Name: "message", Aliases: []string{"m"}, TakesValue: true},
		RemoteCreationField{Name: "message-file", TakesValue: true},
		RemoteCreationField{Name: "no-wait"})
	return &RemoteCreationCatalog{Legacy: true, Version: 1, Commands: map[string][]RemoteCreationField{"add": add, "launch": launch}, Tools: []RemoteCreationTool{{Name: ""}}}
}

func (c *RemoteCreationCatalog) ValidateOptions(opts RemoteAddOptions) error {
	args, err := remoteAddArgs(opts)
	if err != nil {
		return err
	}
	return c.ValidateArgs(args)
}

// ValidateArgs checks every option against the owner's parser before any
// mutation. A value beginning with '-' is still a value, and '--' ends flags.
// Project paths refer to the remote; controller file inputs must first be
// converted to stdin by the public CLI adapter.
func (c *RemoteCreationCatalog) ValidateArgs(args []string) error {
	if c == nil || c.Version != 1 {
		return fmt.Errorf("unsupported remote creation catalog")
	}
	if len(args) == 0 {
		return fmt.Errorf("missing remote creation command")
	}
	fields, ok := c.Commands[args[0]]
	if !ok {
		return fmt.Errorf("unsupported remote creation command %q", args[0])
	}
	known := make(map[string]RemoteCreationField)
	for _, field := range fields {
		known[field.Name] = field
		for _, alias := range field.Aliases {
			known[alias] = field
		}
	}
	positional := 0
	endFlags := false
	remaining := args[1:]
	for len(remaining) > 0 {
		arg := remaining[0]
		remaining = remaining[1:]
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("remote creation argument contains NUL")
		}
		if arg == "--" && !endFlags {
			endFlags = true
			continue
		}
		if endFlags || !strings.HasPrefix(arg, "-") || arg == "-" {
			positional++
			continue
		}
		if strings.HasPrefix(arg, "---") {
			return fmt.Errorf("invalid remote creation flag %q", arg)
		}
		name, value, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		field, ok := known[name]
		if !ok {
			return fmt.Errorf("unsupported remote creation field --%s; update the remote", name)
		}
		if field.TakesValue && !inline {
			if len(remaining) == 0 {
				return fmt.Errorf("remote creation field --%s needs a value", name)
			}
			value = remaining[0]
			remaining = remaining[1:]
			if strings.ContainsRune(value, 0) {
				return fmt.Errorf("remote creation argument contains NUL")
			}
		}
		if !field.TakesValue && inline {
			if _, err := strconv.ParseBool(value); err != nil {
				return fmt.Errorf("invalid boolean for remote creation field --%s", name)
			}
		}
		switch name {
		case "account":
			if err := validateRemoteAccount(value); err != nil {
				return err
			}
		case "message-file":
			if value != "-" {
				return fmt.Errorf("remote --message-file must use forwarded stdin, not a controller file path")
			}
		case "ssh", "remote-path":
			return fmt.Errorf("remote creation field --%s cannot create a nested controller SSH wrapper", name)
		case "extra-arg":
			if err := ValidateClaudeExtraArgToken(value); err != nil {
				return err
			}
		}
	}
	if positional > 1 {
		return fmt.Errorf("remote creation accepts only one project path; use --additional-path for extra repositories")
	}
	return nil
}

func validateRemoteAccount(account string) error {
	if strings.ContainsAny(account, `/\`) || strings.HasPrefix(account, "~") || strings.HasPrefix(account, ".") {
		return fmt.Errorf("account %q looks like a config directory; pass a named account slot that exists in the remote's config.toml (local config directories and credentials are never copied to a remote)", account)
	}
	return nil
}

// FlagValue reads the effective value from argv already accepted by ValidateArgs.
// It skips value tokens and respects '--', so a literal '--attach' query is data.
func (c *RemoteCreationCatalog) FlagValue(args []string, wanted string) (string, bool) {
	fields := make(map[string]RemoteCreationField)
	if len(args) == 0 {
		return "", false
	}
	for _, f := range c.Commands[args[0]] {
		fields[f.Name] = f
		for _, a := range f.Aliases {
			fields[a] = f
		}
	}
	value, found := "", false
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		name, v, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		field := fields[name]
		if field.TakesValue && !inline && i+1 < len(args) {
			i++
			v = args[i]
		} else if !field.TakesValue && !inline {
			v = "true"
		}
		if name == wanted || field.Name == wanted {
			value, found = v, true
		}
	}
	return value, found
}
