#!/bin/bash
# Remove the AgentDeck.app menu-bar wrapper and its LaunchAgent. macOS only.
set -euo pipefail

[ "$(uname)" = "Darwin" ] || { echo "menubar app is macOS only" >&2; exit 1; }

LABEL="com.agentdeck.menubar"
APP_DIR="${AGENTDECK_MENUBAR_APP_DIR:-$HOME/Applications}"
APP="$APP_DIR/AgentDeck.app"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

if [ -f "$PLIST" ]; then
	launchctl unload "$PLIST" 2>/dev/null || true
	rm -f "$PLIST"
	echo "Removed LaunchAgent $LABEL"
fi
if [ -d "$APP" ]; then
	rm -rf "$APP"
	echo "Removed $APP"
fi
echo "✅ Uninstalled the menu-bar app. (Sessions and daemons are untouched.)"
