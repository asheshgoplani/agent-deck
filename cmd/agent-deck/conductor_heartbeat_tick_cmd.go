package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

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
	prev := session.LoadHeartbeatTickState(name)
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
