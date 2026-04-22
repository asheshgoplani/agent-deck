#!/bin/bash
# Escalation stub for agent-deck watchdog.
#
# Called as: escalate.sh <severity> <message>
#
# Severities: cascade, rate-limit, restart-failed
#
# Default behavior: log to syslog. Wire to Telegram bot later by editing
# this file and adding a curl to the appropriate bot endpoint.

SEVERITY="${1:-unknown}"
MESSAGE="${2:-no message}"

logger -t agent-deck-watchdog -p user.warning "[$SEVERITY] $MESSAGE"

# Optional: desktop notification if on a graphical session
if [ -n "$DISPLAY" ] && command -v notify-send >/dev/null 2>&1; then
    notify-send -u critical "agent-deck watchdog [$SEVERITY]" "$MESSAGE"
fi

# TODO: wire Telegram escalation here once bot token story is finalized.
# Example (leave commented until activated):
# curl -s -X POST "https://api.telegram.org/bot$TOKEN/sendMessage" \
#   -d "chat_id=$CHAT_ID" \
#   -d "text=🚨 [agent-deck watchdog] [$SEVERITY] $MESSAGE"

exit 0
