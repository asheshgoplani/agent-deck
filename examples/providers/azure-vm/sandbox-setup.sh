#!/bin/bash
# Runs on a new azure-vm sandbox, as the login user, once, after boot. The
# provider runs it over ssh, with its input on stdin. It makes the machine
# ready for a first agent session:
#   - the project folder ~/<project_name> (a git clone, a folder the provider
#     already copied in, or the built-in demo project),
#   - Claude Code onboarding done and the project trusted, so the first
#     session shows Claude's prompt instead of the theme and trust screens,
#   - optional: a Claude Code login token, written to a 0600 env file that
#     agent-deck sources for claude sessions ([claude].env_file).
#
# Copied from the docker example's sandbox-setup.sh (same input and output);
# only the demo project's location differs (cloud-init writes it to
# /opt/agent-deck-sandbox/demo-project).
#
# Input: key=value lines on stdin (never argv, so nothing shows in ps):
#   project_name=<name>   folder under $HOME (default demo)
#   project_git=<url>     clone this repo into the folder
#   project_copied=1      the provider already copied a folder there
#   claude_token=<token>  a CLAUDE_CODE_OAUTH_TOKEN (claude setup-token),
#                         at most once, the whole token on this one line
# Output: nothing on stdout; one summary line on stderr. The token is never
# printed, and neither is the content of a line this script does not know
# (it could be a token that ended up on a line of its own).
set -euo pipefail
umask 077

die() {
	printf 'sandbox setup: %s\n' "$*" >&2
	exit 1
}

name=demo
git_url=""
copied=0
token=""
seen_token=0
while IFS= read -r line || [ -n "$line" ]; do
	case "$line" in
	project_name=*) name="${line#*=}" ;;
	project_git=*) git_url="${line#*=}" ;;
	project_copied=1) copied=1 ;;
	claude_token=*)
		[ "$seen_token" = 0 ] || die "more than one claude_token line (not shown)"
		seen_token=1
		token="${line#*=}"
		;;
	"") ;;
	*) die "unknown setup line (content not shown)" ;;
	esac
done

[[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "invalid project name"
project="$HOME/$name"

if [ "$copied" = 1 ]; then
	[ -d "$project" ] || die "project folder $project was not copied in"
elif [ -n "$git_url" ]; then
	[[ "$git_url" =~ ^(https://|http://|ssh://|git@)[^[:space:]]+$ ]] || die "project_git must be an https://, http://, ssh:// or git@ URL"
	[ ! -e "$project" ] || die "$project already exists"
	GIT_TERMINAL_PROMPT=0 git clone --quiet --depth 1 -- "$git_url" "$project" </dev/null >&2 ||
		die "git clone of the project failed"
else
	[ ! -e "$project" ] || die "$project already exists"
	cp -a /opt/agent-deck-sandbox/demo-project "$project"
	(cd "$project" && git init --quiet && git add . && git commit --quiet -m "demo project") >&2 ||
		die "cannot make the demo project a git repo"
fi

# Claude Code reads its onboarding and per-folder trust state from
# ~/.claude.json. Mark onboarding done and trust the project (and the home
# folder) so the first session goes straight to the prompt. agent-deck on
# the sandbox starts claude with --dangerously-skip-permissions by default
# (dangerous_mode), so the one-time "Bypass Permissions mode" screen is
# accepted too: this machine is the disposable sandbox that screen asks for.
trust='{"hasTrustDialogAccepted":true,"hasCompletedProjectOnboarding":true,"allowedTools":[]}'
printf '{"hasCompletedOnboarding":true,"theme":"dark","autoUpdates":false,"numStartups":1,"bypassPermissionsModeAccepted":true,"projects":{"%s":%s,"%s":%s}}\n' \
	"$HOME" "$trust" "$project" "$trust" >"$HOME/.claude.json"

login=none
if [ -n "$token" ]; then
	# Accept a bare token or a CLAUDE_CODE_OAUTH_TOKEN=... line.
	token="${token#export }"
	token="${token#CLAUDE_CODE_OAUTH_TOKEN=}"
	token="${token//$'\r'/}"
	[[ "$token" =~ ^[A-Za-z0-9._~+/=-]{20,4096}$ ]] || die "the claude token does not look like a token (not shown)"
	install -d -m 0700 "$HOME/.config/agent-deck-sandbox" "$HOME/.config/agent-deck"
	# printf is a shell builtin: the token is never in any process's argv.
	printf 'export CLAUDE_CODE_OAUTH_TOKEN=%s\n' "$token" >"$HOME/.config/agent-deck-sandbox/claude.env"
	chmod 0600 "$HOME/.config/agent-deck-sandbox/claude.env"
	# agent-deck sources [claude].env_file before it starts claude, so the
	# token reaches every claude session agent-deck starts here.
	printf '[claude]\nenv_file = "~/.config/agent-deck-sandbox/claude.env"\n' >"$HOME/.config/agent-deck/config.toml"
	login=token
fi
token=""

printf 'sandbox setup: project %s, claude %s, login %s\n' "$project" "$(claude --version 2>/dev/null | head -n 1 || echo missing)" "$login" >&2
