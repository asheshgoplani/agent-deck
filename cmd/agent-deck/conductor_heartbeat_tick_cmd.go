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
	fs := flag.NewFlagSet("conductor heartbeat-tick", flag.ContinueOnError)
	rules := fs.String("rules", "", "Resolved HEARTBEAT_RULES.md path (re-read is requested only when it changes)")
	commitMessage := fs.String("commit-message", "", "Persist this exact message after a successful send")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck conductor heartbeat-tick <name> [--rules PATH]")
		fmt.Println()
		fmt.Println("Print the heartbeat message for a conductor, or nothing when nothing changed.")
		fs.PrintDefaults()
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		exitCLI(1)
	}
	name := fs.Arg(0)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "heartbeat-tick: %v\n", err)
		exitCLI(1)
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		fmt.Fprintf(os.Stderr, "heartbeat-tick: %v\n", err)
		exitCLI(1)
	}
	session.RefreshInstancesForCLIStatus(instances)

	in := session.HeartbeatTickInput{
		Name:       name,
		RulesPath:  *rules,
		RulesStamp: session.HeartbeatRulesStamp(*rules),
	}
	conductorInst := session.FindConductorInstance(instances, name)
	prev := session.LoadHeartbeatTickState(name)
	for _, inst := range instances {
		if conductorInst != nil && inst == conductorInst {
			n, digest, err := session.InboxSnapshot(inst.ID)
			in.InboxPending = n
			in.InboxDigest = digest
			in.InboxError = in.InboxError || err != nil
			continue
		}
		if inst.ResolveConductorName() != "" ||
			(inst.GroupPath != name && !strings.HasPrefix(inst.GroupPath, name+"/")) {
			continue
		}
		// Archived children neither count nor wake the conductor, as in
		// statusInstances. Their completion records still arrive via the inbox.
		if inst.IsArchived() {
			continue
		}
		_ = inst.UpdateStatus()
		in.Sessions = append(in.Sessions, session.HeartbeatSessionView{
			Title: inst.Title, Status: inst.Status, Path: inst.ProjectPath,
		})
	}

	msg, next := session.BuildHeartbeatTick(in, prev)
	if *commitMessage != "" {
		// The send being committed may itself have drained the inbox
		// (#2944), so prefer the state the printing tick recorded.
		committed, err := session.CommitHeartbeatTickPending(name, *commitMessage)
		if err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat-tick: save state: %v\n", err)
			exitCLI(1)
		}
		if committed {
			return
		}
		if msg != *commitMessage {
			fmt.Fprintln(os.Stderr, "heartbeat-tick: message is not the last printed tick; state not saved")
			exitCLI(1)
		}
		if err := session.SaveHeartbeatTickState(name, next); err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat-tick: save state: %v\n", err)
			exitCLI(1)
		}
		return
	}
	if msg == "" {
		if err := session.SaveHeartbeatTickState(name, next); err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat-tick: save state: %v\n", err)
		}
	}
	if msg != "" {
		if err := session.SaveHeartbeatTickPending(name, msg, next); err != nil {
			fmt.Fprintf(os.Stderr, "heartbeat-tick: save pending state: %v\n", err)
		}
		fmt.Println(msg)
	}
}
