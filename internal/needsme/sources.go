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
		p := p
		srcs = append(srcs, Source{Profile: p, Fetch: func(ctx context.Context) ([]Row, error) {
			return runList(ctx, selfPath, p)
		}})
	}
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
				rows := make([]Row, 0, len(infos))
				for _, i := range infos {
					rows = append(rows, Row{ID: i.ID, Title: i.Title, Group: i.Group, Profile: rc.GetProfile(),
						Status: i.Status, Substate: i.Substate, SubstateDetail: i.SubstateDetail,
						LastActivityAt: i.LastActivityAt, CreatedAt: i.CreatedAt, Archived: i.Archived})
				}
				return rows, nil
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
