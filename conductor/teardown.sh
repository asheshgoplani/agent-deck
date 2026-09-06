#!/usr/bin/env bash
set -euo pipefail

# Conductor Teardown Script (Multi-Profile)
# Stops the bridge daemon and per-profile conductor sessions.

AGENT_DECK_DIR="${HOME}/.agent-deck"
CONDUCTOR_DIR="${AGENT_DECK_DIR}/conductor"
CONFIG_PATH="${AGENT_DECK_DIR}/config.toml"
PLIST_NAME="com.agentdeck.conductor-bridge"
PLIST_PATH="${HOME}/Library/LaunchAgents/${PLIST_NAME}.plist"
GHW_PLIST_PATH="${HOME}/Library/LaunchAgents/com.agentdeck.gh-watcher.plist"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${CYAN}[info]${NC}  $*"; }
ok()    { echo -e "${GREEN}[ok]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[warn]${NC}  $*"; }

# --------------------------------------------------------------------------
# Step 1: Stop launchd daemon
# --------------------------------------------------------------------------

if [[ -f "${PLIST_PATH}" ]]; then
    info "Stopping bridge daemon..."
    launchctl unload "${PLIST_PATH}" 2>/dev/null || true
    rm -f "${PLIST_PATH}"
    ok "Daemon stopped and plist removed"
else
    info "No daemon plist found (already removed)"
fi

# Opt-in GitHub watcher unit (issue #2134); present only when it was enabled.
if [[ -f "${GHW_PLIST_PATH}" ]]; then
    launchctl unload "${GHW_PLIST_PATH}" 2>/dev/null || true
    /usr/bin/trash "${GHW_PLIST_PATH}" 2>/dev/null || true
    ok "gh-watcher unit stopped and plist moved to Trash"
fi

# --------------------------------------------------------------------------
# Step 2: Read profiles and stop conductor sessions
# --------------------------------------------------------------------------

# Extract profiles from config (fall back to default if parsing fails)
PROFILES="default"
if command -v python3 >/dev/null 2>&1 && [[ -f "${CONFIG_PATH}" ]]; then
    PROFILES=$(python3 -c "
import toml, sys
config = toml.load('${CONFIG_PATH}')
profiles = config.get('conductor', {}).get('profiles', ['default'])
print(' '.join(profiles))
" 2>/dev/null || echo "default")
fi

if command -v agent-deck >/dev/null 2>&1; then
    for PROFILE in ${PROFILES}; do
        SESSION_TITLE="conductor-${PROFILE}"
        info "Stopping conductor session: ${SESSION_TITLE} (profile: ${PROFILE})"
        agent-deck -p "${PROFILE}" session stop "${SESSION_TITLE}" 2>/dev/null || true
        ok "  ${SESSION_TITLE} stopped"
    done
fi

# --------------------------------------------------------------------------
# Step 3: Ask about cleanup
# --------------------------------------------------------------------------

echo ""
echo -e "${YELLOW}Optional cleanup:${NC}"
echo "  1. Remove conductor directories (per-profile dirs in ${CONDUCTOR_DIR})"
echo "  2. Remove [conductor] section from config.toml"
echo "  3. Remove conductor sessions from agent-deck"
echo ""
read -rp "Remove conductor directories? (y/N): " REMOVE_DIR
read -rp "Remove conductor sessions from agent-deck? (y/N): " REMOVE_SESSION

if [[ "${REMOVE_DIR}" =~ ^[Yy] ]]; then
    for PROFILE in ${PROFILES}; do
        PROFILE_DIR="${CONDUCTOR_DIR}/${PROFILE}"
        if [[ -d "${PROFILE_DIR}" ]]; then
            rm -rf "${PROFILE_DIR}"
            ok "Removed ${PROFILE_DIR}"
        fi
    done
    # Remove bridge.py and bridge.log from base conductor dir
    rm -f "${CONDUCTOR_DIR}/bridge.py" "${CONDUCTOR_DIR}/bridge.log"
    # Remove conductor dir if empty
    rmdir "${CONDUCTOR_DIR}" 2>/dev/null && ok "Removed empty ${CONDUCTOR_DIR}" || true
else
    info "Conductor directories kept at ${CONDUCTOR_DIR}"
fi

if [[ "${REMOVE_SESSION}" =~ ^[Yy] ]]; then
    for PROFILE in ${PROFILES}; do
        SESSION_TITLE="conductor-${PROFILE}"
        agent-deck -p "${PROFILE}" remove "${SESSION_TITLE}" 2>/dev/null && \
            ok "Removed session ${SESSION_TITLE} from ${PROFILE}" || \
            warn "Could not remove ${SESSION_TITLE} (remove manually from TUI)"
    done
fi

echo ""
echo -e "${GREEN}Teardown complete.${NC}"
echo ""
echo "Note: The [conductor] section in config.toml was NOT removed."
echo "Edit ${AGENT_DECK_DIR}/config.toml manually if you want to remove it."
