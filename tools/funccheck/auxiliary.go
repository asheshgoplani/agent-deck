package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// auxiliary exercises persisted state through the public CLI. Its SSH shim
// executes the real binary in a separate disposable profile, without a network.
func (s *suite) auxiliary() {
	s.auxGroups()
	s.auxWorktree()
	s.auxAccountsAndMCP()
	s.auxRemote()
	s.auxUpdate()
	help, err := s.cmd("--help")
	if err != nil {
		s.run("Health availability", "Discover the optional health command", func() (string, error) { return help, err })
	} else if !regexp.MustCompile(`(?m)^[\t ]+health(?:[\t ]|$)`).MatchString(help) {
		s.skip("Health", "Read runtime health as JSON", "health is not in this build")
	} else {
		s.run("Health", "Read runtime health as JSON", func() (string, error) {
			out, err := s.cmd("health", "--json")
			if err != nil {
				return out, err
			}
			var data map[string]any
			if err := auxDecode(out, &data); err != nil {
				return out, err
			}
			if len(data) == 0 {
				return out, fmt.Errorf("health returned an empty object")
			}
			return "Runtime health returned a nonempty JSON report", nil
		})
	}
}

func auxDecode(out string, into any) error {
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), into); err == nil {
		return nil
	}
	// Diagnostic warnings may precede JSON on the combined command output.
	for i := 0; i < len(out); i++ {
		if out[i] != '{' && out[i] != '[' {
			continue
		}
		if json.Unmarshal([]byte(strings.TrimSpace(out[i:])), into) == nil {
			return nil
		}
	}
	return fmt.Errorf("expected JSON, received %q", out)
}

func (s *suite) auxRecord(args ...string) (map[string]any, error) {
	out, err := s.cmd(args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, out)
	}
	var record map[string]any
	if err := auxDecode(out, &record); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *suite) auxGroups() {
	const group = "funccheck-moved"
	s.run("Group create", "Create a group and find it in group list", func() (string, error) {
		if out, err := s.cmd("group", "create", group); err != nil {
			return out, err
		}
		out, err := s.cmd("group", "list", "--json")
		if err != nil {
			return out, err
		}
		var listing struct {
			Groups []map[string]any `json:"groups"`
		}
		if err := auxDecode(out, &listing); err != nil {
			return out, err
		}
		if listing.Groups == nil {
			return out, fmt.Errorf("group list omitted its groups array")
		}
		for _, g := range listing.Groups {
			if g["path"] == group || g["name"] == group {
				return "The new group appears in the group list", nil
			}
		}
		return out, fmt.Errorf("created group missing from list")
	})
	original := ""
	s.run("Group move", "Move a session into the group, then move it back", func() (string, error) {
		rec, err := s.auxRecord("session", "show", "--json", s.sessionID)
		if err != nil {
			return "", err
		}
		original, _ = rec["group"].(string)
		if out, err := s.cmd("group", "move", s.sessionID, group); err != nil {
			return out, err
		}
		rec, err = s.auxRecord("session", "show", "--json", s.sessionID)
		if err != nil {
			return "", err
		}
		if rec["group"] != group {
			return "", fmt.Errorf("session did not move: %v", rec["group"])
		}
		if original == "" {
			original = "default"
		}
		if out, err := s.cmd("group", "move", s.sessionID, original); err != nil {
			return out, err
		}
		rec, err = s.auxRecord("session", "show", "--json", s.sessionID)
		if err != nil {
			return "", err
		}
		if rec["group"] != original {
			return "", fmt.Errorf("session did not move back")
		}
		return "The session moved into the group and back to its original group", nil
	})
	s.run("Group delete", "Delete the empty group and verify it disappears", func() (string, error) {
		if out, err := s.cmd("group", "delete", group); err != nil {
			return out, err
		}
		out, err := s.cmd("group", "list", "--json")
		if err != nil {
			return out, err
		}
		var listing struct {
			Groups []map[string]any `json:"groups"`
		}
		if err := auxDecode(out, &listing); err != nil {
			return out, err
		}
		if listing.Groups == nil {
			return out, fmt.Errorf("group list omitted its groups array")
		}
		for _, g := range listing.Groups {
			if g["path"] == group || g["name"] == group {
				return out, fmt.Errorf("deleted group still listed")
			}
		}
		return "The deleted group is absent from group list", nil
	})
}

func (s *suite) auxWorktree() {
	const title = "funccheck-worktree"
	path := ""
	safePath := false
	s.run("Worktree create", "Create a new branch worktree session and inspect Git registration", func() (string, error) {
		if out, err := s.cmd("add", "-t", title, "-c", "shell", "-w", "funccheck-branch", "-b", s.project); err != nil {
			return out, err
		}
		rec, err := s.auxRecord("session", "show", "--json", title)
		if err != nil {
			return "", err
		}
		path, _ = rec["path"].(string)
		if path == "" || path == s.project {
			return "", fmt.Errorf("worktree session did not receive a separate directory")
		}
		relative, err := filepath.Rel(s.root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("worktree escaped disposable root: %s", path)
		}
		safePath = true
		out, err := s.exec("git", "worktree", "list", "--porcelain")
		if err != nil {
			return out, err
		}
		if !strings.Contains(out, "worktree "+path+"\n") || !strings.Contains(out, "branch refs/heads/funccheck-branch") {
			return out, fmt.Errorf("Git worktree or branch is missing")
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			return "", err
		}
		return "A separate directory and branch appear in Git and the session record", nil
	})
	s.run("Worktree cleanup", "Remove the session with --prune-worktree and verify both disappear", func() (string, error) {
		if !safePath {
			return "", fmt.Errorf("worktree creation did not establish a safe cleanup target")
		}
		if out, err := s.cmd("session", "remove", "--force", "--prune-worktree", title); err != nil {
			return out, err
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			return "", fmt.Errorf("worktree directory still exists or cannot be inspected: %v", err)
		}
		out, err := s.exec("git", "worktree", "list", "--porcelain")
		if err != nil {
			return out, err
		}
		if strings.Contains(out, "worktree "+path+"\n") {
			return out, fmt.Errorf("worktree remains registered")
		}
		if err := s.requireMissing(title); err != nil {
			return "", err
		}
		return "Session, worktree directory, and Git registration are gone", nil
	})
}

func (s *suite) auxAccountsAndMCP() {
	const title = "funccheck-account"
	setupErr := func() error {
		a := filepath.Join(s.root, "fixture-account-a")
		b := filepath.Join(s.root, "fixture-account-b")
		for _, p := range []string{a, b} {
			if err := os.MkdirAll(p, 0700); err != nil {
				return err
			}
		}
		config := fmt.Sprintf("\n[profiles.fixture_a.claude]\nconfig_dir = %q\n[profiles.fixture_b.claude]\nconfig_dir = %q\n[mcps.funccheck]\ncommand = \"cat\"\nargs = []\n", a, b)
		f, err := os.OpenFile(filepath.Join(s.root, ".agent-deck", "config.toml"), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(config)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}()
	s.run("Accounts list", "List two configured disposable account directories", func() (string, error) {
		if setupErr != nil {
			return "", setupErr
		}
		out, err := s.cmd("accounts", "--json")
		if err != nil {
			return out, err
		}
		var rows []struct {
			Name      string `json:"name"`
			Exists    bool   `json:"exists"`
			ConfigDir string `json:"config_dir"`
		}
		if err := auxDecode(out, &rows); err != nil {
			return out, err
		}
		found := map[string]bool{}
		for _, r := range rows {
			if r.Exists && strings.HasPrefix(r.ConfigDir, s.root+string(filepath.Separator)) {
				found[r.Name] = true
			}
		}
		if !found["fixture_a"] || !found["fixture_b"] {
			return out, fmt.Errorf("both disposable account slots must exist")
		}
		return "Both fake account slots are listed with existing directories", nil
	})
	s.run("Account switch", "Switch a fresh Claude session between fake account directories and back", func() (string, error) {
		if setupErr != nil {
			return "", setupErr
		}
		if out, err := s.cmd("add", "-t", title, "-c", "claude", "--account", "fixture_a", s.project); err != nil {
			return out, err
		}
		for _, account := range []string{"fixture_b", "fixture_a"} {
			if out, err := s.cmd("session", "switch-account", "--no-restart", title, account); err != nil {
				return out, err
			}
			rec, err := s.auxRecord("session", "show", "--json", title)
			if err != nil {
				return "", err
			}
			if rec["account"] != account {
				return "", fmt.Errorf("stored account = %v, want %s", rec["account"], account)
			}
		}
		return "The stored account changed to fixture_b and back to fixture_a; no login or conversation migration was needed", nil
	})
	for _, action := range []string{"attach", "detach"} {
		action := action
		s.run("MCP "+action, "Run mcp "+action+" and reread attached MCPs", func() (string, error) {
			if setupErr != nil {
				return "", setupErr
			}
			if out, err := s.cmd("mcp", action, title, "funccheck"); err != nil {
				return out, err
			}
			rec, err := s.auxRecord("mcp", "attached", "--json", title)
			if err != nil {
				return "", err
			}
			found, err := mcpMembership(rec, "funccheck")
			if err != nil {
				return "", err
			}
			if found != (action == "attach") {
				return "", fmt.Errorf("MCP membership after %s is %v", action, found)
			}
			return "The attached MCP record reflects " + action, nil
		})
	}
	s.run("Account fixture cleanup", "Remove the disposable account session", func() (string, error) {
		if out, err := s.cmd("session", "remove", "--force", title); err != nil {
			return out, err
		}
		if err := s.requireMissing(title); err != nil {
			return "", err
		}
		return "The disposable account session no longer resolves", nil
	})
}

func (s *suite) auxRemote() {
	// The CLI currently sweeps a fixed SSH control directory, outside HOME.
	// Refuse the entire lane if any preexisting socket could be touched.
	entries, err := os.ReadDir("/tmp/agent-deck-ssh")
	if (err != nil && !os.IsNotExist(err)) || len(entries) > 0 {
		for _, name := range []string{"Remote list", "Remote create", "Remote sessions", "Remote switch", "Remote cleanup"} {
			s.unknown(name, "Exercise the synthetic SSH endpoint", "Existing or unreadable /tmp/agent-deck-ssh prevents an isolated remote check; run in the disposable Docker container")
		}
		return
	}
	const name = "funccheck-remote"
	const profile = "funccheck_remote"
	const title = "funccheck-remote-session"
	// Only this synthetic host is accepted. The last SSH argument is the actual
	// CLI's shell-quoted command; execute it against the isolated profile.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	prefix := quote(s.bin)
	shim := "#!/bin/sh\nallowed=false\nfor arg do\n [ \"$arg\" = \"funccheck.invalid\" ] && allowed=true\n last=$arg\ndone\n[ \"$allowed\" = true ] || exit 97\nprefix=" + quote(prefix) + "\ncase \"$last\" in \"$prefix \"*) ;; *) exit 98 ;; esac\nexec /bin/sh -c \"$last\"\n"

	setupErr := os.WriteFile(filepath.Join(s.root, "bin", "ssh"), []byte(shim), 0700)
	s.run("Remote list", "Register a synthetic SSH remote and read its record", func() (string, error) {
		if setupErr != nil {
			return "", setupErr
		}
		if out, err := s.cmd("remote", "add", "--agent-deck-path", s.bin, "--profile", profile, name, "funccheck.invalid"); err != nil {
			return out, err
		}
		out, err := s.cmd("remote", "list", "--json")
		if err != nil {
			return out, err
		}
		rows, err := remoteCollection(out, false, "name")
		if err != nil {
			return out, err
		}
		for _, r := range rows {
			if r["name"] == name && r["host"] == "funccheck.invalid" && r["profile"] == profile {
				return "The synthetic SSH endpoint is listed with its isolated profile", nil
			}
		}
		return out, fmt.Errorf("configured remote missing from list")
	})
	s.run("Remote create", "Forward add through fake SSH to a separate profile", func() (string, error) {
		if setupErr != nil {
			return "", setupErr
		}
		if out, err := s.cmd("remote", name, "add", "-t", title, "-c", "shell", s.project); err != nil {
			return out, err
		}
		rec, err := s.auxRecord("-p", profile, "session", "show", "--json", title)
		if err != nil {
			return "", err
		}
		if rec["title"] != title || rec["profile"] != profile {
			return "", fmt.Errorf("remote creation did not persist in remote profile")
		}
		if err := s.requireMissing(title); err != nil {
			return "", fmt.Errorf("remote session isolation: %w", err)
		}
		return "The real binary created the session only in the synthetic remote profile", nil
	})
	s.run("Remote sessions", "Fetch the created session through fake SSH", func() (string, error) {
		out, err := s.cmd("remote", "sessions", "--json", name)
		if err != nil {
			return out, err
		}
		rows, err := remoteCollection(out, true, "id", "title")
		if err != nil {
			return out, err
		}
		for _, r := range rows {
			if r["title"] == title {
				return "The remote session appears in the fetched fleet", nil
			}
		}
		return out, fmt.Errorf("remote list omitted the created session")
	})
	help, err := s.cmd("session", "switch", "--help")
	if err != nil {
		s.unknown("Remote switch", "Probe remote switching support", "Switch help probe failed: "+err.Error())
	} else if !strings.Contains(help, "--remote") {
		s.skip("Remote switch", "Switch a session on a synthetic remote", "Remote switch flag is not in this build")
	} else {
		s.unknown("Remote switch", "Switch a session on a synthetic remote", "Remote switch is advertised but this runner has no verified invocation contract for this build")
	}
	s.run("Remote cleanup", "Remove the synthetic session and endpoint, then verify absence", func() (string, error) {
		if out, err := s.cmd("-p", profile, "session", "remove", "--force", title); err != nil {
			return out, err
		}
		out, err := s.cmd("remote", "sessions", "--json", name)
		if err != nil {
			return out, err
		}
		sessions, err := remoteCollection(out, true, "id", "title")
		if err != nil {
			return out, err
		}
		for _, r := range sessions {
			if r["title"] == title {
				return out, fmt.Errorf("removed remote session still listed")
			}
		}
		// remote sessions --json also hides transport errors as null on older
		// builds. Require independent absence in the isolated owning profile.
		if err := s.requireMissing(title, "-p", profile); err != nil {
			return "", err
		}
		if out, err := s.cmd("remote", "remove", name); err != nil {
			return out, err
		}
		out, err = s.cmd("remote", "list", "--json")
		if err != nil {
			return out, err
		}
		var remotes []map[string]any
		// Older CLIs emit this exact empty-state text even with --json.
		const emptyRemotes = "No remotes configured.\n\nAdd one with: agent-deck remote add <name> <user@host>"
		if out != emptyRemotes {
			remotes, err = remoteCollection(out, false, "name")
			if err != nil {
				return out, err
			}
		}
		for _, r := range remotes {
			if r["name"] == name {
				return out, fmt.Errorf("removed remote still listed")
			}
		}
		return "The remote session and endpoint are absent; cleanup used the isolated server profile", nil
	})
}

// Keep the update kill switch enabled even when probing a future binary. A
// missing cached answer is unknown, never evidence of a successful cache read.
func (s *suite) auxUpdate() {
	const marker = "9999.0.0"
	latest := ""
	probed := false
	s.run("Update offline command", "Seed update cache and invoke update --check --json with network checks disabled", func() (string, error) {
		cacheRoot := filepath.Join(s.root, ".cache")
		for _, entry := range s.env {
			if value, ok := strings.CutPrefix(entry, "XDG_CACHE_HOME="); ok {
				cacheRoot = value
			}
		}
		cacheDir := filepath.Join(cacheRoot, "agent-deck")
		if err := os.MkdirAll(cacheDir, 0700); err != nil {
			return "", err
		}
		cache, err := json.Marshal(map[string]any{"checked_at": time.Now().UTC(), "latest_version": marker, "current_version": "0.0.0", "download_url": "https://funccheck.invalid/archive", "release_url": "https://funccheck.invalid/release"})
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(cacheDir, "update-cache.json"), cache, 0600); err != nil {
			return "", err
		}
		rec, err := s.auxRecord("update", "--check", "--json")
		if err != nil {
			return "", err
		}
		current, ok := rec["current"].(string)
		if !ok || current == "" {
			return "", fmt.Errorf("update response lacks current version")
		}
		if _, ok := rec["available"].(bool); !ok {
			return "", fmt.Errorf("update response lacks availability boolean")
		}
		latest, _ = rec["latest"].(string)
		probed = true
		return "The offline update command returned a version and availability JSON record", nil
	})
	if probed && latest == marker {
		s.run("Update cache", "Verify the seeded cache version reaches the user", func() (string, error) {
			return "The reported latest version matches the unique seeded cache version", nil
		})
	} else {
		s.unknown("Update cache", "Verify the seeded cache version reaches the user", "The offline probe did not return the seeded cached version. Cache-only behavior cannot be confirmed with the update kill switch enabled.")
	}
}

// A timeout, database failure, or malformed response cannot establish absence.
func (s *suite) requireMissing(id string, globalArgs ...string) error {
	if id == "" {
		return errors.New("cannot check absence without a session identifier")
	}
	args := append(globalArgs, "session", "show", "--json", id)
	out, err := s.cmd(args...)
	if err == nil {
		return fmt.Errorf("session %s still resolves", id)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		return fmt.Errorf("session absence was not established: %w", err)
	}
	var response map[string]any
	if decodeErr := json.Unmarshal([]byte(out), &response); decodeErr != nil {
		return fmt.Errorf("session absence returned invalid JSON: %w", decodeErr)
	}
	if response["code"] != "NOT_FOUND" || response["success"] != false {
		return fmt.Errorf("expected explicit NOT_FOUND failure for %s, received %s", id, out)
	}
	return nil
}

// sessionListed validates the list's shape before making an exact ID lookup.
func (s *suite) sessionListed(id string) (bool, error) {
	if id == "" {
		return false, errors.New("cannot inspect list without a session ID")
	}
	out, err := s.cmd("list", "--json")
	if err != nil {
		return false, err
	}
	// Legacy CLI returns this exact empty state even with --json. Every
	// removal also requires the independent structured NOT_FOUND response.
	if strings.TrimSpace(out) == "No sessions found in profile 'default'." {
		return false, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "[") {
		return false, fmt.Errorf("session list must be a JSON array, received %q", out)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		return false, err
	}
	found := false
	for _, row := range rows {
		if row.ID == "" {
			return false, errors.New("session list contains a record without an ID")
		}
		if row.ID == id {
			found = true
		}
	}
	return found, nil
}

// Missing membership fields are unknown data, never evidence of detachment.
func mcpMembership(record map[string]any, wanted string) (bool, error) {
	found := false
	for _, key := range []string{"local", "global", "project"} {
		value, exists := record[key]
		if !exists {
			return false, fmt.Errorf("MCP record omitted %s membership", key)
		}
		if value == nil {
			continue
		}
		names, ok := value.([]any)
		if !ok {
			return false, fmt.Errorf("MCP %s membership is not an array", key)
		}
		for _, value := range names {
			name, ok := value.(string)
			if !ok {
				return false, fmt.Errorf("MCP %s membership contains a non-string", key)
			}
			if name == wanted {
				found = true
			}
		}
	}
	return found, nil
}

// Validate every identifying field before any lookup, including rows after a
// match. Only remote sessions has a source-supported null empty collection.
func remoteCollection(out string, allowNull bool, fields ...string) ([]map[string]any, error) {
	var rows []map[string]any
	if err := auxDecode(out, &rows); err != nil {
		return nil, err
	}
	if rows == nil && !allowNull {
		return nil, fmt.Errorf("remote list must return an array")
	}
	for _, row := range rows {
		for _, field := range fields {
			value, ok := row[field].(string)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("remote collection record omitted valid %s", field)
			}
		}
	}
	return rows, nil
}
