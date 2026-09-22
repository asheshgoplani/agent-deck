package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleConductorHeartbeatTick prints the delta-only heartbeat message for one
// conductor, or nothing when this tick has nothing new to say (issue #2348).
// heartbeat.sh sends whatever this prints; empty output means no turn at all.
func handleConductorHeartbeatTick(profile string, args []string) {
	fs := flag.NewFlagSet("conductor heartbeat-tick", flag.ExitOnError)
	rules := fs.String("rules", "", "Resolved HEARTBEAT_RULES.md path (re-read is requested only when it changes)")
	commitMessage := fs.String("commit-message", "", "Persist this exact message after a successful send")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck conductor heartbeat-tick <name> [--rules PATH]")
		fmt.Println()
		fmt.Println("Print the heartbeat message for a conductor, or nothing when nothing changed.")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}
	name := fs.Arg(0)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "heartbeat-tick: %v\n", err)
		os.Exit(1)
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Fprintf(os.Stderr, "heartbeat-tick: %v\n", err)
		os.Exit(1)
	}
	session.RefreshInstancesForCLIStatus(instances)

	in := session.HeartbeatTickInput{
		Name:       name,
		RulesPath:  *rules,
		RulesStamp: session.HeartbeatRulesStamp(*rules),
	}
	conductorTitle := session.ConductorSessionTitle(name)
	var conductorID string
	for _, inst := range instances {
		if inst.Title == conductorTitle {
			conductorID = inst.ID
			break
		}
	}
	prev := session.LoadHeartbeatTickState(name)
	if *commitMessage == "" && conductorID != "" {
		if err := pullHeartbeatRemotes(profile, conductorID, instances); err != nil {
			in.RemoteError = true
			if !prev.RemoteFailed {
				fmt.Fprintf(os.Stderr, "heartbeat-tick: remote pull: %v\n", err)
			}
		}
		if in.RemoteError != prev.RemoteFailed {
			prev.RemoteFailed = in.RemoteError
			if err := session.SaveHeartbeatTickState(name, prev); err != nil {
				fmt.Fprintf(os.Stderr, "heartbeat-tick: save remote health: %v\n", err)
			}
		}
	}
	for _, inst := range instances {
		if inst.Title == conductorTitle {
			n, digest, err := session.InboxSnapshot(inst.ID)
			in.InboxPending = n
			in.InboxDigest = digest
			in.InboxError = in.InboxError || err != nil
			continue
		}
		if strings.HasPrefix(inst.Title, "conductor-") ||
			(inst.GroupPath != name && !strings.HasPrefix(inst.GroupPath, name+"/")) {
			continue
		}
		_ = inst.UpdateStatus()
		in.Sessions = append(in.Sessions, session.HeartbeatSessionView{
			Title: inst.Title, Status: inst.Status, Path: inst.ProjectPath,
		})
	}

	msg, next := session.BuildHeartbeatTick(in, prev)
	if *commitMessage != "" {
		if msg != *commitMessage {
			fmt.Fprintln(os.Stderr, "heartbeat-tick: inputs changed before commit; next tick will retry")
			os.Exit(1)
		}
		if err := session.SaveHeartbeatTickState(name, next); err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat-tick: save state: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if msg == "" {
		if err := session.SaveHeartbeatTickState(name, next); err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat-tick: save state: %v\n", err)
		}
	}
	if msg != "" {
		fmt.Println(msg)
	}
}

func pullHeartbeatRemotes(profile, conductorID string, instances []*session.Instance) error {
	config, err := session.LoadUserConfig()
	if err != nil {
		return err
	}
	children := heartbeatRemoteChildren(conductorID, instances, config.Remotes)
	if len(children) == 0 {
		return nil
	}
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	return pullHeartbeatRemoteNames(names, func(name string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*config.Remotes[name].GetCommandTimeout()+5*time.Second)
		args := []string{"-p", profile, "remote", "drain", name, "--into", conductorID, "--json"}
		for _, childID := range children[name] {
			args = append(args, "--child-id", childID)
		}
		output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
		}
		return nil
	})
}

func heartbeatRemoteChildren(conductorID string, instances []*session.Instance, remotes map[string]session.RemoteConfig) map[string][]string {
	children := make(map[string][]string)
	for _, inst := range instances {
		if inst.ParentSessionID != conductorID || inst.SSHHost == "" {
			continue
		}
		for name, remote := range remotes {
			if remote.Host == inst.SSHHost {
				children[name] = append(children[name], inst.ID)
			}
		}
	}
	for name := range children {
		sort.Strings(children[name])
	}
	return children
}

func pullHeartbeatRemoteNames(names []string, drain func(string) error) error {
	sort.Strings(names)
	var failures []error
	for _, name := range names {
		if err := drain(name); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
