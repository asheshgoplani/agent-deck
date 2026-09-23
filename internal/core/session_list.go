package core

import (
	"context"
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// SessionListIn is the input of session.list.
type SessionListIn struct {
	Profile           string `json:"profile" doc:"Profile to list; ignored with all_profiles"`
	AllProfiles       bool   `json:"all_profiles,omitempty" doc:"List every profile"`
	IncludeSuperseded bool   `json:"include_superseded,omitempty" doc:"Include archived source rows retained for cross-harness recovery"`
	LiveStatus        bool   `json:"live_status,omitempty" doc:"Refresh status and fill the live fields (status, substate, viewers, model); single profile only"`
}

// SessionListOut is the output of session.list.
type SessionListOut struct {
	Profile  string       `json:"profile,omitempty" doc:"Resolved profile name (single-profile listing)"`
	Sessions []SessionRow `json:"sessions" doc:"Sessions of the single profile; empty with all_profiles"`
	// Stats feeds the opt-in --stats diagnostic on the direct CLI. Wall
	// time and process-local tmux call counts are not stable command data,
	// so they are deliberately absent from canonical envelopes.
	Stats *session.ListStats `json:"-"`
	// Profiles and ProfileCount are set for all_profiles. ProfileCount counts
	// every profile found, including ones that failed to load or are empty.
	Profiles     []ProfileSessions `json:"profiles,omitempty"`
	ProfileCount int               `json:"profile_count,omitempty"`
}

// ProfileSessions is one profile of an all-profiles listing.
type ProfileSessions struct {
	Profile  string       `json:"profile"`
	Sessions []SessionRow `json:"sessions"`
}

// SessionRow is one listed session. Status is the raw session status; the
// fields marked live are only filled with live_status.
//
// Field order and tags mirror the sessionJSON struct of buildListJSON in
// cmd/agent-deck/main.go: `list --json` marshals these rows directly, so a
// reorder here changes its bytes.
type SessionRow struct {
	ID                string         `json:"id"`
	ParentSessionID   string         `json:"parent_session_id,omitempty"`
	ParentProjectPath string         `json:"parent_project_path,omitempty"`
	Title             string         `json:"title"`
	Path              string         `json:"path"`
	Group             string         `json:"group"`
	Tool              string         `json:"tool"`
	Account           string         `json:"account"`
	Command           string         `json:"command,omitempty"`
	ModelID           string         `json:"model_id,omitempty" doc:"live"`
	Model             string         `json:"model,omitempty" doc:"live"`
	ModelVersion      string         `json:"model_version,omitempty" doc:"live"`
	Status            string         `json:"status"`
	Substate          string         `json:"substate,omitempty" doc:"live"`
	SubstateDetail    string         `json:"substate_detail,omitempty" doc:"live"`
	TmuxSession       string         `json:"tmux_session,omitempty" doc:"live"`
	Profile           string         `json:"profile"`
	CreatedAt         time.Time      `json:"created_at"`
	SSHHost           string         `json:"ssh_host,omitempty"`
	SSHRemotePath     string         `json:"ssh_remote_path,omitempty"`
	Channels          []string       `json:"channels,omitempty" doc:"live"`
	ExtraArgs         []string       `json:"extra_args,omitempty" doc:"live"`
	Color             string         `json:"color,omitempty" doc:"live"`
	Favorite          bool           `json:"favorite,omitempty" doc:"live"`
	Archived          bool           `json:"archived" doc:"live"`
	ArchivedAt        time.Time      `json:"archived_at,omitempty" doc:"live"`
	SupersededBy      string         `json:"superseded_by,omitempty" doc:"live"`
	Supersedes        string         `json:"supersedes,omitempty" doc:"live"`
	CodexSessionID    string         `json:"codex_session_id,omitempty"`
	ResolvedCodexHome string         `json:"resolved_codex_home,omitempty"`
	LastActivityAt    string         `json:"last_activity_at,omitempty" doc:"live"`
	Viewers           *[]tmux.Viewer `json:"viewers,omitempty" doc:"live; absent when tmux could not be asked"`
}

func sessionList(ctx context.Context, in SessionListIn) (SessionListOut, error) {
	if in.AllProfiles {
		return listAllProfiles(in)
	}
	storage, err := session.NewStorageWithProfile(in.Profile)
	if err != nil {
		return SessionListOut{}, &Error{Code: CodeStorage, Message: fmt.Sprintf("failed to initialize storage: %v", err), Cause: err}
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return SessionListOut{}, &Error{Code: CodeStorage, Message: fmt.Sprintf("failed to load sessions: %v", err), Cause: err}
	}
	if !in.IncludeSuperseded {
		instances = session.VisibleInstances(instances)
	}
	out := SessionListOut{Profile: storage.Profile(), Sessions: []SessionRow{}}
	if len(instances) == 0 {
		return out, nil
	}
	if !in.LiveStatus {
		for _, inst := range instances {
			out.Sessions = append(out.Sessions, staticSessionRow(inst, instances, out.Profile))
		}
		return out, nil
	}

	// Warm the pane-title cache and hook statuses so the listing reports the
	// status the TUI and /api/menu do (#610), and record the pass (#2331).
	started := time.Now()
	tmuxBefore := tmux.SubprocessStarts()
	session.RefreshInstancesForCLIStatus(instances)
	out.Sessions = liveSessionRows(ctx, out.Profile, instances)
	elapsed := time.Since(started)
	tmuxCalls := tmux.SubprocessStarts() - tmuxBefore
	health.RecordStatusPass(elapsed, len(instances), tmuxCalls)
	out.Stats = &session.ListStats{StatusPassMS: elapsed.Milliseconds(), TmuxCalls: tmuxCalls, Sessions: len(instances)}
	return out, nil
}

// staticSessionRow fills the stored fields only; no tmux or pane access.
func staticSessionRow(inst *session.Instance, instances []*session.Instance, profile string) SessionRow {
	return SessionRow{
		ID:                inst.ID,
		ParentSessionID:   inst.ParentSessionID,
		ParentProjectPath: parentProjectPath(inst, instances),
		Title:             inst.Title,
		Path:              inst.ProjectPath,
		Group:             inst.GroupPath,
		Tool:              inst.Tool,
		Account:           inst.Account,
		Command:           inst.Command,
		Status:            string(inst.Status),
		Profile:           profile,
		CreatedAt:         inst.CreatedAt,
		SSHHost:           inst.SSHHost,
		SSHRemotePath:     inst.SSHRemotePath,
		CodexSessionID:    inst.CodexSessionID,
		ResolvedCodexHome: inst.ResolvedCodexHome(),
	}
}

// liveSessionRows refreshes each session's status and fills every field.
// Callers warm the status caches first.
func liveSessionRows(ctx context.Context, profile string, instances []*session.Instance) []SessionRow {
	rows := make([]SessionRow, len(instances))
	viewers := session.ViewersByTmuxSession(ctx, instances)
	var pass session.StatusUpdatePass
	for i, inst := range instances {
		// Listings need live status, not native-session discovery.
		_ = pass.UpdateStatusOnly(inst)
		// The substate read is this pass's one pane capture and can settle
		// the status (hook lag): take it before the status.
		substate := string(inst.Substate())
		row := staticSessionRow(inst, instances, profile)
		row.Substate = substate
		row.SubstateDetail = inst.SubstateDetail()
		row.Channels = inst.Channels
		row.ExtraArgs = inst.ExtraArgs
		row.Color = inst.Color
		row.Favorite = inst.Favorite
		row.Archived = inst.IsArchived()
		row.ArchivedAt = inst.ArchivedAt
		row.SupersededBy = inst.SupersededBy
		row.Supersedes = inst.Supersedes
		row.LastActivityAt = inst.DisplayLastActivityTime().Format(time.RFC3339Nano)
		if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
			row.TmuxSession = tmuxSess.Name
			if v, known := viewers[tmuxSess.Name]; known {
				row.Viewers = &v
			}
		}
		if modelInfo := inst.LaunchModelInfo(); modelInfo.ModelID != "" {
			row.ModelID = modelInfo.ModelID
			row.Model = modelInfo.ModelID
			row.ModelVersion = modelInfo.Version
		}
		rows[i] = row
	}
	return rows
}

// listAllProfiles loads every profile's stored rows. A profile that fails to
// open or load is skipped, as the CLI always did, but still counted.
func listAllProfiles(in SessionListIn) (SessionListOut, error) {
	profiles, err := session.ListProfiles()
	if err != nil {
		return SessionListOut{}, &Error{Code: CodeStorage, Message: fmt.Sprintf("failed to list profiles: %v", err), Cause: err}
	}
	out := SessionListOut{ProfileCount: len(profiles), Profiles: []ProfileSessions{}, Sessions: []SessionRow{}}
	for _, name := range profiles {
		storage, err := session.NewStorageWithProfile(name)
		if err != nil {
			continue
		}
		instances, _, err := storage.LoadWithGroups()
		if err != nil {
			continue
		}
		if !in.IncludeSuperseded {
			instances = session.VisibleInstances(instances)
		}
		ps := ProfileSessions{Profile: name, Sessions: make([]SessionRow, 0, len(instances))}
		for _, inst := range instances {
			ps.Sessions = append(ps.Sessions, staticSessionRow(inst, instances, name))
		}
		out.Profiles = append(out.Profiles, ps)
	}
	return out, nil
}

// parentProjectPath reports the parent path represented by the stored parent
// id, recovering it from the parent row for older rows that lack it.
func parentProjectPath(inst *session.Instance, instances []*session.Instance) string {
	if inst == nil || inst.ParentSessionID == "" {
		return ""
	}
	if inst.ParentProjectPath != "" {
		return inst.ParentProjectPath
	}
	for _, candidate := range instances {
		if candidate.ID == inst.ParentSessionID {
			return candidate.ProjectPath
		}
	}
	return ""
}
