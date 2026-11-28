package needsme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// DefaultSources is one source per local profile (asked through this same
// binary's read-only `list --json`, so the status verdict is the one every
// other surface shows) plus one per configured remote. selfPath is the
// agent-deck binary to run for local profiles; fallbackProfile is used when
// no profile directory exists yet.
func DefaultSources(selfPath, fallbackProfile string) []Source {
	profiles, perr := session.ListProfiles()
	var srcs []Source
	if perr != nil {
		// One unreadable profile directory must not hide the others: ask
		// every readable one and report the failure.
		profiles = readableProfiles()
		srcs = append(srcs, Source{Fetch: func(context.Context) ([]Row, error) { return nil, errFailed }})
	}
	if len(profiles) == 0 && perr == nil {
		profiles = []string{fallbackProfile}
	}
	for _, p := range profiles {
		srcs = append(srcs, localSource(selfPath, p))
	}
	return append(srcs, remoteSources()...)
}

// ProfileSources is the voice desk's list: the one given (current) local
// profile plus every configured remote. Voice never targets a session of
// another local profile, so those are never asked; `needs-me` keeps
// DefaultSources (every local profile, as docs/voice.md says).
func ProfileSources(selfPath, profile string) []Source {
	return append([]Source{localSource(selfPath, profile)}, remoteSources()...)
}

func localSource(selfPath, profile string) Source {
	return Source{Profile: profile, Fetch: func(ctx context.Context) ([]Row, error) {
		return runList(ctx, selfPath, profile)
	}}
}

// remoteSources is one source per configured remote, in name order.
func remoteSources() []Source {
	var srcs []Source
	cfg, err := session.LoadUserConfig()
	if err == nil && cfg != nil {
		names := make([]string, 0, len(cfg.Remotes))
		for n := range cfg.Remotes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			n, rc := n, cfg.Remotes[n]
			srcs = append(srcs, Source{Host: n, Profile: rc.GetProfile(), Fetch: func(ctx context.Context) ([]Row, error) {
				infos, _, err := session.NewSSHRunner(n, rc).FetchSessions(ctx)
				if err != nil {
					return nil, remoteFetchError(err)
				}
				return remoteRows(infos, rc.GetProfile()), nil
			}})
		}
	}
	return srcs
}

func runList(ctx context.Context, self, profile string) ([]Row, error) {
	cmd := exec.CommandContext(ctx, self, "-p", profile, "list", "--json", "--no-archived")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errFailed
	}
	out := bytes.TrimSpace(stdout.Bytes())
	if len(out) == 0 || out[0] != '[' {
		return nil, nil // "No sessions found" style output
	}
	var rows []Row
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, ErrBadReply
	}
	for i := range rows {
		rows[i].Profile = profile
	}
	return rows, nil
}

// readableProfiles lists the profile directories that can be read, for when
// session.ListProfiles failed on one of them.
func readableProfiles() []string {
	dir, err := session.GetProfilesDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, f := range []string{"state.db", "sessions.json"} {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), f)); err == nil {
				out = append(out, e.Name())
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// remoteFetchError maps a remote fetch error: a reply that is not valid
// JSON is ErrBadReply ("bad-reply"); anything else passes through.
func remoteFetchError(err error) error {
	if errors.Is(err, session.ErrRemoteSessionsParse) {
		return ErrBadReply
	}
	return err
}

// remoteRows turns a remote's session list into rows for Collect.
func remoteRows(infos []session.RemoteSessionInfo, profile string) []Row {
	rows := make([]Row, 0, len(infos))
	for _, i := range infos {
		rows = append(rows, Row{ID: i.ID, Title: i.Title, Group: i.Group, Profile: profile,
			Status: i.Status, Substate: i.Substate, SubstateDetail: i.SubstateDetail,
			LastActivityAt: i.LastActivityAt, WaitingSince: i.WaitingSince, CreatedAt: i.CreatedAt, Archived: i.Archived,
			ConductorName: i.ConductorName, Tool: i.Tool})
	}
	return rows
}
