package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Registry-backed adapters for `list` and `group list`. Output is
// byte-identical to handleList, handleListAllProfiles and handleGroupList
// (asserted by core_cli_equivalence_test.go).

func cliList(profile string, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	var jsonOutput jsonModeFlag
	fs.Var(&jsonOutput, "json", "Output as JSON")
	allProfiles := fs.Bool("all", false, "List sessions from all profiles")
	includeSuperseded := fs.Bool("include-superseded", false, "Include archived source rows retained for cross-harness recovery")
	// Undocumented: SSHRunner passes this on its own remote invocation
	// (#2331) so a slow `list --json` names its own status-pass duration.
	statsFlag := fs.Bool(strings.TrimLeft(session.ListStatsFlag, "-"), false, "")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck list [options]")
		fmt.Println()
		fmt.Println("List all sessions.")
		fmt.Println("ACCOUNT shows the quoted stored account slot, not a resolved account or login identity.")
		fmt.Println(`JSON always includes the raw "account" string, including "" when no slot is stored.`)
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck list                    # List from default profile")
		fmt.Println("  agent-deck -p work list            # List from 'work' profile")
		fmt.Println("  agent-deck list --all              # List from all profiles")
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	if !*allProfiles {
		ensureTmuxInPathOrExit()
	}

	res := runCore(core.IDSessionList, core.SessionListIn{
		Profile:           profile,
		AllProfiles:       *allProfiles,
		IncludeSuperseded: *includeSuperseded,
		LiveStatus:        jsonOutput.enabled() && !*allProfiles,
	}, nil)
	if jsonOutput.envelope() {
		printEnvelope(res)
		if res.Err != nil {
			os.Exit(1)
		}
		return
	}
	if res.Err != nil {
		fmt.Printf("Error: %s\n", core.AsError(res.Err).Message)
		os.Exit(1)
	}
	listed := res.Out.(core.SessionListOut)

	if *allProfiles {
		renderListAllProfiles(listed, jsonOutput.enabled())
		return
	}

	if len(listed.Sessions) == 0 {
		if jsonOutput.enabled() {
			// Still a list: --json consumers decode stdout as an array.
			fmt.Println("[]")
			return
		}
		fmt.Printf("No sessions found in profile '%s'.\n", listed.Profile)
		return
	}

	if jsonOutput.enabled() {
		if *statsFlag && listed.Stats != nil {
			emitListStats(time.Duration(listed.Stats.StatusPassMS)*time.Millisecond, listed.Stats.TmuxCalls, listed.Stats.Sessions)
		}
		rows := make([]core.SessionRow, len(listed.Sessions))
		for i, row := range listed.Sessions {
			row.Status = StatusString(session.Status(row.Status))
			rows[i] = row
		}
		output, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON output: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(string(append(output, '\n')))
		return
	}

	fmt.Printf("Profile: %s\n\n", listed.Profile)
	printSessionTable(listed.Sessions)
	fmt.Printf("\nTotal: %d sessions\n", len(listed.Sessions))

	printUpdateNotice()
}

// printSessionTable prints the TITLE/GROUP/PATH/ID/ACCOUNT table header and
// rows shared by the single- and all-profile listings.
func printSessionTable(rows []core.SessionRow) {
	fmt.Printf("%-*s %-*s %-*s %-*s %s\n", tableColTitle, "TITLE", tableColGroup, "GROUP", tableColPath, "PATH", tableColIDDisplay, "ID", "ACCOUNT")
	fmt.Println(strings.Repeat("-", tableColTitle+tableColGroup+tableColPath+tableColIDDisplay+5))
	for _, row := range rows {
		idDisplay := row.ID
		if len(idDisplay) > tableColIDDisplay {
			idDisplay = idDisplay[:tableColIDDisplay]
		}
		fmt.Printf("%-*s %-*s %-*s %-*s %s\n",
			tableColTitle, truncate(row.Title, tableColTitle),
			tableColGroup, truncate(row.Group, tableColGroup),
			tableColPath, truncate(row.Path, tableColPath),
			tableColIDDisplay, idDisplay, strconv.Quote(row.Account))
	}
}

// allProfilesSessionJSON is the per-session shape of `list --all --json`.
type allProfilesSessionJSON struct {
	ID                string    `json:"id"`
	ParentSessionID   string    `json:"parent_session_id,omitempty"`
	ParentProjectPath string    `json:"parent_project_path,omitempty"`
	Title             string    `json:"title"`
	Path              string    `json:"path"`
	Group             string    `json:"group"`
	Tool              string    `json:"tool"`
	Account           string    `json:"account"`
	Command           string    `json:"command,omitempty"`
	Profile           string    `json:"profile"`
	CreatedAt         time.Time `json:"created_at"`
	SSHHost           string    `json:"ssh_host,omitempty"`
	SSHRemotePath     string    `json:"ssh_remote_path,omitempty"`
	CodexSessionID    string    `json:"codex_session_id,omitempty"`
	ResolvedCodexHome string    `json:"resolved_codex_home,omitempty"`
}

func renderListAllProfiles(listed core.SessionListOut, jsonOutput bool) {
	if listed.ProfileCount == 0 {
		fmt.Println("No profiles found.")
		return
	}

	if jsonOutput {
		// Non-nil so an empty result marshals as [] rather than null.
		all := []allProfilesSessionJSON{}
		for _, p := range listed.Profiles {
			for _, row := range p.Sessions {
				all = append(all, allProfilesSessionJSON{
					ID:                row.ID,
					ParentSessionID:   row.ParentSessionID,
					ParentProjectPath: row.ParentProjectPath,
					Title:             row.Title,
					Path:              row.Path,
					Group:             row.Group,
					Tool:              row.Tool,
					Account:           row.Account,
					Command:           row.Command,
					Profile:           p.Profile,
					CreatedAt:         row.CreatedAt,
					SSHHost:           row.SSHHost,
					SSHRemotePath:     row.SSHRemotePath,
					CodexSessionID:    row.CodexSessionID,
					ResolvedCodexHome: row.ResolvedCodexHome,
				})
			}
		}
		output, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			fmt.Printf("Error: failed to format JSON output: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(output))
		return
	}

	total := 0
	for _, p := range listed.Profiles {
		if len(p.Sessions) == 0 {
			continue
		}
		fmt.Printf("\n═══ Profile: %s ═══\n\n", p.Profile)
		printSessionTable(p.Sessions)
		fmt.Printf("(%d sessions)\n", len(p.Sessions))
		total += len(p.Sessions)
	}

	fmt.Printf("\n═══════════════════════════════════════\n")
	fmt.Printf("Total: %d sessions across %d profiles\n", total, listed.ProfileCount)
}

func cliGroupList(profile string, args []string) {
	fs := flag.NewFlagSet("group list", flag.ExitOnError)
	var jsonOutput jsonModeFlag
	fs.Var(&jsonOutput, "json", "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck group list [options]")
		fmt.Println()
		fmt.Println("List all groups with session counts.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(jsonOutput.enabled(), quietMode)

	res := runCore(core.IDGroupList, core.GroupListIn{Profile: profile}, nil)
	if res.Err != nil {
		exitCoreError(out, &jsonOutput, res, nil)
	}
	listed := res.Out.(core.GroupListOut)

	if jsonOutput.envelope() {
		printEnvelope(res)
		return
	}
	if jsonOutput.enabled() {
		output, err := json.MarshalIndent(listed, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to format JSON: %v\n", err)
			os.Exit(1)
		}
		if !quietMode {
			fmt.Print(string(append(output, '\n')))
		}
		return
	}

	if listed.TotalGroups == 0 {
		out.Print("No groups found.\n", nil)
		return
	}
	out.Print(renderGroupTable(listed), nil)
}

// renderGroupTable draws the human group tree: indented names with
// ├──/└── connectors, recursive session counts and running/waiting/idle.
func renderGroupTable(listed core.GroupListOut) string {
	var sb strings.Builder
	sb.WriteString("Groups:\n\n")
	sb.WriteString(fmt.Sprintf("%-20s %-10s %s\n", "NAME", "SESSIONS", "STATUS"))
	sb.WriteString(strings.Repeat("-", 50) + "\n")

	printed := make(map[string]bool)
	for _, g := range listed.Flat {
		if printed[g.Path] {
			continue
		}
		indent := strings.Repeat("  ", g.Level)
		prefix := ""
		if g.Level > 0 {
			// Last sibling at its level draws └──, others ├──.
			parent := getParentGroupPath(g.Path)
			isLast := true
			foundCurrent := false
			for _, other := range listed.Flat {
				if getParentGroupPath(other.Path) == parent && other.Level == g.Level {
					if foundCurrent && other.Path != g.Path {
						isLast = false
						break
					}
					if other.Path == g.Path {
						foundCurrent = true
					}
				}
			}
			if isLast {
				prefix = "└── "
			} else {
				prefix = "├── "
			}
		}

		statusStr := ""
		if g.SessionCount > 0 {
			var parts []string
			if g.Status.Running > 0 {
				parts = append(parts, fmt.Sprintf("● %d", g.Status.Running))
			}
			if g.Status.Waiting > 0 {
				parts = append(parts, fmt.Sprintf("◐ %d", g.Status.Waiting))
			}
			if g.Status.Idle > 0 {
				parts = append(parts, fmt.Sprintf("○ %d", g.Status.Idle))
			}
			statusStr = strings.Join(parts, " ")
		}

		name := indent + prefix + g.Name
		sb.WriteString(fmt.Sprintf("%-20s %-10d %s\n", truncateGroupName(name, 20), g.SessionCount, statusStr))
		printed[g.Path] = true
	}

	sb.WriteString(fmt.Sprintf("\nTotal: %d groups, %d sessions\n", listed.TotalGroups, listed.TotalSessions))
	return sb.String()
}
