package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

var slackMemberIDRe = regexp.MustCompile(`^[UW][A-Z0-9]+$`)

// slackNoAllowedUsersWarning is the bridge's startup WARNING, printed by
// setup too so an upgraded install with Slack but no allow-list learns why
// the bot stopped answering before the bridge starts.
const slackNoAllowedUsersWarning = "Slack bridge: no allowed users are set, so every Slack message and command is refused. " +
	"Add your Slack member ID under [conductor.slack] in ~/.agent-deck/config.toml: " +
	`allowed_user_ids = ["U01234567"] (or set allow_all_users = true to let every ` +
	"workspace member in), then restart the bridge."

// slackAccessWarning returns the WARNING for a configured Slack bridge that
// lets nobody in, or "" when Slack is not set up or has an access rule.
func slackAccessWarning(s session.SlackSettings) string {
	if s.BotToken == "" || len(s.AllowedUserIDs) > 0 || s.AllowAllUsers {
		return ""
	}
	return "WARNING: " + slackNoAllowedUsersWarning
}

// slackAccessAttempts bounds the re-asks so a closed or scripted stdin cannot
// loop forever.
const slackAccessAttempts = 3

// promptSlackAccess asks during `conductor setup` who may use the Slack bot
// and records it on s: a list of member IDs, or every workspace member after
// an explicit confirmation. The bridge refuses everyone when neither is set.
func promptSlackAccess(reader *bufio.Reader, out io.Writer, s session.SlackSettings) (session.SlackSettings, error) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Only the Slack users you list can use the bot.")
	fmt.Fprintln(out, "  Find your member ID: Slack profile -> ... (More) -> Copy member ID")
	fmt.Fprintln(out)

	for attempt := 0; attempt < slackAccessAttempts; attempt++ {
		fmt.Fprint(out, "Allowed Slack member IDs, comma separated (U01234567,...): ")
		line, readErr := reader.ReadString('\n')
		answer := strings.TrimSpace(line)

		if answer == "" {
			if readErr != nil {
				break
			}
			fmt.Fprint(out, "No IDs given. Let every member of this Slack workspace use the bot? (y/N): ")
			confirm, _ := reader.ReadString('\n')
			if yesAnswer(strings.ToLower(strings.TrimSpace(confirm))) {
				s.AllowedUserIDs = nil
				s.AllowAllUsers = true
				return s, nil
			}
			continue
		}

		var ids []string
		bad := ""
		for _, id := range strings.Split(answer, ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if !slackMemberIDRe.MatchString(id) {
				bad = id
				break
			}
			ids = append(ids, id)
		}
		if bad != "" {
			fmt.Fprintf(out, "  %q is not a Slack member ID (they look like U01234567)\n", bad)
			continue
		}
		if len(ids) == 0 {
			continue
		}
		s.AllowedUserIDs = ids
		s.AllowAllUsers = false
		return s, nil
	}
	return s, errors.New("allowed Slack member IDs are required (or confirm letting every workspace member in)")
}
