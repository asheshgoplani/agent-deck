#!/usr/bin/env bash
set -euo pipefail

# Conductor Setup Script (Multi-Profile)
# Sets up per-profile conductor sessions, Telegram bridge, and launchd daemon.

AGENT_DECK_DIR="${HOME}/.agent-deck"
CONDUCTOR_DIR="${AGENT_DECK_DIR}/conductor"
CONFIG_PATH="${AGENT_DECK_DIR}/config.toml"
PLIST_NAME="com.agentdeck.conductor-bridge"
PLIST_DIR="${HOME}/Library/LaunchAgents"
PLIST_PATH="${PLIST_DIR}/${PLIST_NAME}.plist"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${CYAN}[info]${NC}  $*"; }
ok()    { echo -e "${GREEN}[ok]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[warn]${NC}  $*"; }
fail()  { echo -e "${RED}[fail]${NC}  $*"; exit 1; }

# --------------------------------------------------------------------------
# Preflight checks
# --------------------------------------------------------------------------

command -v agent-deck >/dev/null 2>&1 || fail "agent-deck not found in PATH"
command -v python3 >/dev/null 2>&1 || fail "python3 not found in PATH"
command -v tmux >/dev/null 2>&1 || fail "tmux not found in PATH"

# --------------------------------------------------------------------------
# Step 1: Create conductor base directory
# --------------------------------------------------------------------------

info "Creating conductor directory: ${CONDUCTOR_DIR}"
mkdir -p "${CONDUCTOR_DIR}"

# Copy bridge script to base conductor directory
cp "${SCRIPT_DIR}/bridge.py" "${CONDUCTOR_DIR}/bridge.py"
chmod +x "${CONDUCTOR_DIR}/bridge.py"
ok "bridge.py installed"

# Copy default heartbeat rules (profiles can override with their own)
cp "${SCRIPT_DIR}/HEARTBEAT_RULES.md" "${CONDUCTOR_DIR}/HEARTBEAT_RULES.md"
ok "HEARTBEAT_RULES.md installed (default)"

# --------------------------------------------------------------------------
# Step 2: Install Python dependencies
# --------------------------------------------------------------------------

info "Installing Python dependencies..."
python3 -m pip install --quiet --user aiogram toml 2>/dev/null || {
    warn "pip install --user failed, trying without --user..."
    python3 -m pip install --quiet aiogram toml 2>/dev/null || {
        fail "Could not install Python dependencies (aiogram, toml). Install manually: pip3 install aiogram toml"
    }
}
ok "Python dependencies installed (aiogram, toml)"

# --------------------------------------------------------------------------
# Step 3: Configure Telegram bot
# --------------------------------------------------------------------------

# Check if [conductor] section already exists
if grep -q '^\[conductor\]' "${CONFIG_PATH}" 2>/dev/null; then
    info "[conductor] section already exists in config.toml"
    if grep -q 'token\s*=' "${CONFIG_PATH}" 2>/dev/null; then
        ok "Telegram config found, skipping prompt"
    else
        warn "No token found. You'll need to add it manually."
    fi
else
    echo ""
    echo -e "${CYAN}Telegram Bot Setup${NC}"
    echo "You need a Telegram bot token and your user ID."
    echo ""
    echo "1. Message @BotFather on Telegram -> /newbot -> copy the token"
    echo "2. Message @userinfobot on Telegram -> copy your user ID"
    echo ""

    read -rp "Telegram bot token: " TG_TOKEN
    read -rp "Your Telegram user ID: " TG_USER_ID

    if [[ -z "${TG_TOKEN}" || -z "${TG_USER_ID}" ]]; then
        fail "Both token and user ID are required"
    fi

    # Backup config before modifying
    cp "${CONFIG_PATH}" "${CONFIG_PATH}.backup-$(date +%Y%m%d-%H%M%S)"

    # Append conductor config
    cat >> "${CONFIG_PATH}" << TOML

# ============================================================================
# Conductor (Meta-Agent Orchestration)
# ============================================================================
# The conductor manages persistent Claude Code sessions that monitor all other
# sessions per profile, auto-respond when possible, and escalate via Telegram.

[conductor]
enabled = true
heartbeat_interval = 15
profiles = ["default", "work"]

[conductor.telegram]
token = "${TG_TOKEN}"
user_id = ${TG_USER_ID}
TOML

    ok "Added [conductor] section to config.toml"
fi

# --------------------------------------------------------------------------
# Step 4: Read profiles from config and set up per-profile directories
# --------------------------------------------------------------------------

# Extract profiles from config using Python (handles TOML parsing properly)
PROFILES=$(python3 -c "
import toml, sys
config = toml.load('${CONFIG_PATH}')
profiles = config.get('conductor', {}).get('profiles', ['default'])
print(' '.join(profiles))
" 2>/dev/null || echo "default")

info "Setting up conductor for profiles: ${PROFILES}"

for PROFILE in ${PROFILES}; do
    PROFILE_DIR="${CONDUCTOR_DIR}/${PROFILE}"
    SESSION_TITLE="conductor-${PROFILE}"

    info "Setting up profile: ${PROFILE}"
    mkdir -p "${PROFILE_DIR}"

    # Copy and substitute CLAUDE.md template
    if [[ -f "${PROFILE_DIR}/CLAUDE.md" ]]; then
        warn "  CLAUDE.md already exists for ${PROFILE}, backing up"
        cp "${PROFILE_DIR}/CLAUDE.md" "${PROFILE_DIR}/CLAUDE.md.bak"
    fi
    sed "s/{PROFILE}/${PROFILE}/g" "${SCRIPT_DIR}/conductor-claude.md" > "${PROFILE_DIR}/CLAUDE.md"
    ok "  CLAUDE.md installed for ${PROFILE} (profile substituted)"

    # Register conductor session in agent-deck for this profile
    info "  Registering conductor session: ${SESSION_TITLE} (profile: ${PROFILE})"

    if agent-deck -p "${PROFILE}" list --json 2>/dev/null | python3 -c "
import sys, json
data = json.load(sys.stdin)
sessions = data.get('sessions', data) if isinstance(data, dict) else data
found = any(s.get('title') == '${SESSION_TITLE}' for s in sessions)
sys.exit(0 if found else 1)
" 2>/dev/null; then
        ok "  Session ${SESSION_TITLE} already registered in ${PROFILE}"
    else
        agent-deck -p "${PROFILE}" add "${PROFILE_DIR}" -t "${SESSION_TITLE}" -c claude -g "infra" 2>/dev/null && \
            ok "  Session ${SESSION_TITLE} registered in ${PROFILE}" || \
            warn "  Could not register ${SESSION_TITLE} (add manually from TUI)"
    fi
done

# --------------------------------------------------------------------------
# Step 5: Install launchd plist
# --------------------------------------------------------------------------

info "Installing launchd daemon..."
mkdir -p "${PLIST_DIR}"

# Resolve python3 path for plist
PYTHON3_PATH="$(command -v python3)"

# Generate plist from template with variable substitution
sed \
    -e "s|__PYTHON3__|${PYTHON3_PATH}|g" \
    -e "s|__BRIDGE_PATH__|${CONDUCTOR_DIR}/bridge.py|g" \
    -e "s|__LOG_PATH__|${CONDUCTOR_DIR}/bridge.log|g" \
    -e "s|__HOME__|${HOME}|g" \
    "${SCRIPT_DIR}/${PLIST_NAME}.plist" > "${PLIST_PATH}"

ok "Plist installed at ${PLIST_PATH}"

# Load the daemon
if launchctl list | grep -q "${PLIST_NAME}" 2>/dev/null; then
    info "Reloading existing daemon..."
    launchctl unload "${PLIST_PATH}" 2>/dev/null || true
fi
launchctl load "${PLIST_PATH}"
ok "Daemon loaded and running"

# --------------------------------------------------------------------------
# Done
# --------------------------------------------------------------------------

echo ""
echo -e "${GREEN}Conductor setup complete!${NC}"
echo ""
echo "Profiles configured: ${PROFILES}"
echo ""
echo "Next steps:"
for PROFILE in ${PROFILES}; do
    echo "  Start conductor:  agent-deck -p ${PROFILE} session start conductor-${PROFILE}"
done
echo ""
echo "  Test from Telegram:   Send /status to your bot"
echo "  View bridge logs:     tail -f ${CONDUCTOR_DIR}/bridge.log"
echo ""
echo "To stop: ./teardown.sh (or: launchctl unload ${PLIST_PATH})"
