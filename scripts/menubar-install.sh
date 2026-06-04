#!/bin/bash
# Assemble AgentDeck.app (menu-bar-only, no Dock icon) and install a LaunchAgent
# so the menu-bar app starts at login. macOS only.
#
# The .app bundle exists purely to set LSUIElement=true — running the
# `agent-deck menubar` binary bare would otherwise put an icon in the Dock.
set -euo pipefail

[ "$(uname)" = "Darwin" ] || { echo "menubar app is macOS only" >&2; exit 1; }

LABEL="com.agentdeck.menubar"
APP_DIR="${AGENTDECK_MENUBAR_APP_DIR:-$HOME/Applications}"
APP="$APP_DIR/AgentDeck.app"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"

# Resolve the agent-deck binary: prefer an explicit override, then a freshly
# built ./build/agent-deck, then whatever is on PATH.
if [ -n "${AGENTDECK_BIN:-}" ]; then
	BIN="$AGENTDECK_BIN"
elif [ -x "./build/agent-deck" ]; then
	BIN="$(cd "$(dirname ./build/agent-deck)" && pwd)/agent-deck"
elif BIN="$(command -v agent-deck 2>/dev/null)"; then
	:
else
	echo "ERROR: agent-deck binary not found. Run 'make build' or set AGENTDECK_BIN." >&2
	exit 1
fi
echo "Using agent-deck binary: $BIN"

# --- assemble the .app bundle ------------------------------------------------
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>Agent Deck</string>
	<key>CFBundleDisplayName</key><string>Agent Deck</string>
	<key>CFBundleIdentifier</key><string>$LABEL</string>
	<key>CFBundleExecutable</key><string>AgentDeck</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleVersion</key><string>1.0</string>
	<key>LSUIElement</key><true/>
	<key>LSMinimumSystemVersion</key><string>11.0</string>
</dict>
</plist>
PLIST

# The bundle executable is a thin launcher that execs the real binary. Baking
# the absolute path avoids PATH surprises when launched by launchd/Finder.
cat > "$APP/Contents/MacOS/AgentDeck" <<LAUNCHER
#!/bin/bash
exec "$BIN" ${AGENTDECK_PROFILE:+-p "$AGENTDECK_PROFILE"} menubar ${AGENTDECK_MENUBAR_LISTEN:+--listen "$AGENTDECK_MENUBAR_LISTEN"}
LAUNCHER
chmod +x "$APP/Contents/MacOS/AgentDeck"
echo "Assembled: $APP"

# --- LaunchAgent (start at login) -------------------------------------------
mkdir -p "$(dirname "$PLIST")"
cat > "$PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$LABEL</string>
	<key>ProgramArguments</key>
	<array>
		<string>$APP/Contents/MacOS/AgentDeck</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key><false/>
	</dict>
	<key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
PLIST

launchctl unload "$PLIST" 2>/dev/null || true
launchctl load "$PLIST"
echo "✅ Installed LaunchAgent $LABEL (starts at login, running now)."
echo "   Quit from the menu-bar 'Quit' item; remove with: make menubar-uninstall"
