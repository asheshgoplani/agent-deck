#!/usr/bin/env bash
#
# Agent Deck Installer
# https://github.com/asheshgoplani/agent-deck
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/install.sh | bash
#
# Options:
#   --name <name>       Custom binary name (default: agent-deck)
#   --dir <path>        Installation directory (default: ~/.local/bin)
#   --version <ver>     Specific version (default: latest)
#   --skip-tmux-config  Skip tmux configuration prompt
#   --non-interactive   Skip all prompts (for CI/automated installs)
#   --pkg-manager <mgr> macOS package manager: 'brew' or 'port' (default: auto-detect)
#
# The installer will:
#   1. Download and install the agent-deck binary
#   2. Check for tmux (offer to install if missing) - REQUIRED
#   3. Check for jq (offer to install if missing) - Optional, for session forking
#   4. Configure ~/.tmux.conf for mouse scrolling & clipboard - Optional
#
# Supported platforms:
#   - macOS (darwin) - arm64 (Apple Silicon), amd64 (Intel)
#   - Linux - arm64, amd64
#   - Windows - via WSL (uses Linux binary, clipboard via clip.exe)
#

set -e

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Defaults
BINARY_NAME="agent-deck"
INSTALL_DIR="${HOME}/.local/bin"
VERSION="latest"
REPO="asheshgoplani/agent-deck"
SKIP_TMUX_CONFIG=false
SKIP_OPTIONAL_DEPS=false

# macOS package manager configuration
MACOS_SUPPORTED_PKG_MGRS=("brew" "port")  # Order matters for preference
MACOS_PKG_MANAGER=""  # Will be auto-detected or set by user

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --name)
            BINARY_NAME="$2"
            shift 2
            ;;
        --dir)
            INSTALL_DIR="$2"
            shift 2
            ;;
        --version)
            VERSION="$2"
            shift 2
            ;;
        --skip-tmux-config)
            SKIP_TMUX_CONFIG=true
            shift
            ;;
        --non-interactive)
            SKIP_TMUX_CONFIG=true
            SKIP_OPTIONAL_DEPS=true
            shift
            ;;
        --pkg-manager)
            if [[ -z "${2:-}" || "${2:0:1}" == "-" ]]; then
                echo -e "${RED}Error: --pkg-manager requires a value (${MACOS_SUPPORTED_PKG_MGRS[*]})${NC}"
                exit 1
            fi
            MACOS_PKG_MANAGER="$2"
            # Validate against supported package managers
            valid=false
            for mgr in "${MACOS_SUPPORTED_PKG_MGRS[@]}"; do
                if [[ "$MACOS_PKG_MANAGER" == "$mgr" ]]; then
                    valid=true
                    break
                fi
            done
            if [[ "$valid" != "true" ]]; then
                echo -e "${RED}Error: --pkg-manager must be one of: ${MACOS_SUPPORTED_PKG_MGRS[*]}${NC}"
                exit 1
            fi
            shift 2
            ;;
        -h|--help)
            echo "Agent Deck Installer"
            echo ""
            echo "Usage: install.sh [options]"
            echo ""
            echo "Options:"
            echo "  --name <name>       Custom binary name (default: agent-deck)"
            echo "  --dir <path>        Installation directory (default: ~/.local/bin)"
            echo "  --version <ver>     Specific version (default: latest)"
            echo "  --skip-tmux-config  Skip tmux configuration prompt"
            echo "  --non-interactive   Skip all prompts (for CI/automated installs)"
            echo "  --pkg-manager <mgr> macOS package manager: ${MACOS_SUPPORTED_PKG_MGRS[*]} (default: auto-detect)"
            echo "  -h, --help          Show this help message"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            exit 1
            ;;
    esac
done

echo -e "${BLUE}╔════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║        Agent Deck Installer            ║${NC}"
echo -e "${BLUE}╚════════════════════════════════════════╝${NC}"
echo ""

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
IS_WSL=false
case "$OS" in
    darwin) OS="darwin" ;;
    linux)
        OS="linux"
        # Detect WSL (Windows Subsystem for Linux)
        if grep -qi microsoft /proc/version 2>/dev/null || [[ -n "$WSL_DISTRO_NAME" ]]; then
            IS_WSL=true
        fi
        ;;
    *)
        echo -e "${RED}Error: Unsupported operating system: $OS${NC}"
        echo "Agent Deck only supports macOS and Linux."
        exit 1
        ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *)
        echo -e "${RED}Error: Unsupported architecture: $ARCH${NC}"
        exit 1
        ;;
esac

if [[ "$IS_WSL" == "true" ]]; then
    echo -e "Detected: ${GREEN}${OS}/${ARCH}${NC} (WSL - Windows Subsystem for Linux)"
else
    echo -e "Detected: ${GREEN}${OS}/${ARCH}${NC}"
fi

# macOS-specific package manager configuration
if [[ "$OS" == "darwin" ]]; then
    # Package manager configuration
    declare -A MACOS_PKG_MGR_NAMES=(
        ["brew"]="Homebrew"
        ["port"]="MacPorts"
    )

    declare -A MACOS_PKG_MGR_COMMANDS=(
        ["brew"]="brew"
        ["port"]="port"
    )

    declare -A MACOS_PKG_MGR_INSTALL_CMDS=(
        ["brew"]="brew install"
        ["port"]="sudo port install"
    )

    declare -A MACOS_PKG_MGR_LINKS=(
        ["brew"]="https://brew.sh"
        ["port"]="https://www.macports.org/install.php"
    )
fi

# Detect or select macOS package manager
detect_macos_package_manager() {
    # If user specified a package manager, verify it's available
    if [[ -n "$MACOS_PKG_MANAGER" ]]; then
        local cmd="${MACOS_PKG_MGR_COMMANDS[$MACOS_PKG_MANAGER]}"
        local name="${MACOS_PKG_MGR_NAMES[$MACOS_PKG_MANAGER]}"
        local link="${MACOS_PKG_MGR_LINKS[$MACOS_PKG_MANAGER]}"

        if ! command -v "$cmd" &> /dev/null; then
            echo -e "${RED}Error: $name not found but --pkg-manager=$MACOS_PKG_MANAGER was specified${NC}"
            echo "Install $name: $link"
            exit 1
        fi
        echo -e "Package manager: ${GREEN}${name}${NC} (user specified)"
        return
    fi

    # Auto-detect: check for available package managers
    local available_mgrs=()
    for mgr in "${MACOS_SUPPORTED_PKG_MGRS[@]}"; do
        if command -v "${MACOS_PKG_MGR_COMMANDS[$mgr]}" &> /dev/null; then
            available_mgrs+=("$mgr")
        fi
    done

    # Handle based on how many are available
    if [[ ${#available_mgrs[@]} -eq 0 ]]; then
        # None available
        MACOS_PKG_MANAGER=""
        echo -e "${YELLOW}No package manager detected (Homebrew or MacPorts)${NC}"
        echo "You'll need to install dependencies manually or install a package manager first:"
        for mgr in "${MACOS_SUPPORTED_PKG_MGRS[@]}"; do
            echo "  • ${MACOS_PKG_MGR_NAMES[$mgr]}: ${MACOS_PKG_MGR_LINKS[$mgr]}"
        done
    elif [[ ${#available_mgrs[@]} -eq 1 ]]; then
        # Only one available
        MACOS_PKG_MANAGER="${available_mgrs[0]}"
        echo -e "Package manager: ${GREEN}${MACOS_PKG_MGR_NAMES[$MACOS_PKG_MANAGER]}${NC} (auto-detected)"
    else
        # Multiple available - ask user to choose
        echo -e "${YELLOW}Multiple package managers are installed.${NC}"
        if [[ "$SKIP_OPTIONAL_DEPS" == "true" ]]; then
            # Non-interactive mode: use first in preference order
            MACOS_PKG_MANAGER="${available_mgrs[0]}"
            echo -e "Package manager: ${GREEN}${MACOS_PKG_MGR_NAMES[$MACOS_PKG_MANAGER]}${NC} (auto-selected in non-interactive mode)"
        else
            echo "Which package manager would you like to use?"
            local i=1
            for mgr in "${available_mgrs[@]}"; do
                echo "  $i) ${MACOS_PKG_MGR_NAMES[$mgr]} ($mgr)"
                ((i++))
            done
            read -p "Enter choice [1-${#available_mgrs[@]}]: " -n 1 -r
            echo

            local choice=$((REPLY - 1))
            if [[ $choice -ge 0 && $choice -lt ${#available_mgrs[@]} ]]; then
                MACOS_PKG_MANAGER="${available_mgrs[$choice]}"
                echo -e "Package manager: ${GREEN}${MACOS_PKG_MGR_NAMES[$MACOS_PKG_MANAGER]}${NC}"
            else
                echo -e "${YELLOW}Invalid choice, defaulting to ${MACOS_PKG_MGR_NAMES[${available_mgrs[0]}]}${NC}"
                MACOS_PKG_MANAGER="${available_mgrs[0]}"
            fi
        fi
    fi
}

# Detect package manager on macOS
if [[ "$OS" == "darwin" ]]; then
    detect_macos_package_manager
fi

# Helper function to install packages on macOS
# Usage: install_macos_package <package_name>
# Note: Assumes package has same name across all package managers
# Prerequisite: MACOS_PKG_MANAGER must be set (validated by detect_macos_package_manager)
install_macos_package() {
    local PACKAGE_NAME="$1"
    local install_cmd="${MACOS_PKG_MGR_INSTALL_CMDS[$MACOS_PKG_MANAGER]}"
    local mgr_name="${MACOS_PKG_MGR_NAMES[$MACOS_PKG_MANAGER]}"

    echo -e "Installing $PACKAGE_NAME via $mgr_name..."
    $install_cmd "$PACKAGE_NAME"
}

# Helper function to print manual install commands on macOS
print_macos_manual_install_help() {
    local package_name="$1"
    echo "Install $package_name manually with one of:"
    for mgr in "${MACOS_SUPPORTED_PKG_MGRS[@]}"; do
        echo "  ${MACOS_PKG_MGR_INSTALL_CMDS[$mgr]} $package_name"
    done
}

# Check for tmux and offer to install
if ! command -v tmux &> /dev/null; then
    echo -e "${YELLOW}tmux is not installed.${NC}"
    echo "Agent Deck requires tmux to function."
    echo ""

    # Try to auto-install tmux
    if [[ "$OS" == "darwin" ]]; then
        if [[ -n "$MACOS_PKG_MANAGER" ]]; then
            mgr_name="${MACOS_PKG_MGR_NAMES[$MACOS_PKG_MANAGER]}"
            read -p "Install tmux via $mgr_name? [Y/n] " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Nn]$ ]]; then
                install_macos_package "tmux"
            fi
        else
            print_macos_manual_install_help "tmux"
        fi
    else
        # Linux - try apt, dnf, or pacman
        if command -v apt-get &> /dev/null; then
            read -p "Install tmux via apt? [Y/n] " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Nn]$ ]]; then
                echo -e "Installing tmux..."
                sudo apt-get update && sudo apt-get install -y tmux
            fi
        elif command -v dnf &> /dev/null; then
            read -p "Install tmux via dnf? [Y/n] " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Nn]$ ]]; then
                echo -e "Installing tmux..."
                sudo dnf install -y tmux
            fi
        elif command -v pacman &> /dev/null; then
            read -p "Install tmux via pacman? [Y/n] " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Nn]$ ]]; then
                echo -e "Installing tmux..."
                sudo pacman -S --noconfirm tmux
            fi
        else
            echo "Please install tmux manually:"
            echo "  sudo apt install tmux    # Debian/Ubuntu"
            echo "  sudo dnf install tmux    # Fedora"
            echo "  sudo pacman -S tmux      # Arch"
        fi
    fi

    # Check again after attempted install
    if ! command -v tmux &> /dev/null; then
        echo ""
        read -p "tmux not found. Continue anyway? [y/N] " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    else
        echo -e "${GREEN}tmux installed successfully!${NC}"
    fi
fi

# Check for jq (required for Claude session forking)
if ! command -v jq &> /dev/null && [[ "$SKIP_OPTIONAL_DEPS" != "true" ]]; then
    echo -e "${YELLOW}jq is not installed (optional but recommended).${NC}"
    echo "jq is required for Claude session forking/session ID capture."
    echo ""

    # Try to auto-install jq
    if [[ "$OS" == "darwin" ]]; then
        if [[ -n "$MACOS_PKG_MANAGER" ]]; then
            mgr_name="${MACOS_PKG_MGR_NAMES[$MACOS_PKG_MANAGER]}"
            read -p "Install jq via $mgr_name? [Y/n] " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Nn]$ ]]; then
                install_macos_package "jq"
            fi
        else
            print_macos_manual_install_help "jq"
        fi
    else
        if command -v apt-get &> /dev/null; then
            read -p "Install jq via apt? [Y/n] " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Nn]$ ]]; then
                echo -e "Installing jq..."
                sudo apt-get install -y jq
            fi
        elif command -v dnf &> /dev/null; then
            read -p "Install jq via dnf? [Y/n] " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Nn]$ ]]; then
                echo -e "Installing jq..."
                sudo dnf install -y jq
            fi
        elif command -v pacman &> /dev/null; then
            read -p "Install jq via pacman? [Y/n] " -n 1 -r
            echo
            if [[ ! $REPLY =~ ^[Nn]$ ]]; then
                echo -e "Installing jq..."
                sudo pacman -S --noconfirm jq
            fi
        else
            echo "Install jq manually for session forking support."
        fi
    fi

    if command -v jq &> /dev/null; then
        echo -e "${GREEN}jq installed successfully!${NC}"
    fi
fi

# Get version
if [[ "$VERSION" == "latest" ]]; then
    echo -e "Fetching latest version..."
    VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')
    if [[ -z "$VERSION" ]]; then
        echo -e "${RED}Error: Could not determine latest version${NC}"
        echo "Please specify a version with --version"
        exit 1
    fi
fi

# Remove 'v' prefix if present for URL
VERSION_NUM="${VERSION#v}"
echo -e "Installing version: ${GREEN}${VERSION}${NC}"

# Download URL
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/agent-deck_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"
echo -e "Downloading from: ${BLUE}${DOWNLOAD_URL}${NC}"

# Create temp directory
TMP_DIR=$(mktemp -d)
trap "rm -rf $TMP_DIR" EXIT

# Download and extract
echo -e "Downloading..."
if ! curl -fsSL "$DOWNLOAD_URL" -o "$TMP_DIR/agent-deck.tar.gz"; then
    echo -e "${RED}Error: Download failed${NC}"
    echo "URL: $DOWNLOAD_URL"
    echo ""
    echo "This could mean:"
    echo "  - The version doesn't exist"
    echo "  - The release hasn't been published yet"
    echo "  - Network issues"
    echo ""
    echo "Try building from source instead:"
    echo "  git clone https://github.com/${REPO}.git"
    echo "  cd agent-deck && make install"
    exit 1
fi

echo -e "Extracting..."
tar -xzf "$TMP_DIR/agent-deck.tar.gz" -C "$TMP_DIR"

# Create install directory
mkdir -p "$INSTALL_DIR"

# Install binary
echo -e "Installing to ${GREEN}${INSTALL_DIR}/${BINARY_NAME}${NC}"
mv "$TMP_DIR/agent-deck" "$INSTALL_DIR/$BINARY_NAME"
chmod +x "$INSTALL_DIR/$BINARY_NAME"

# Check if install directory is in PATH
if [[ ":$PATH:" != *":$INSTALL_DIR:"* ]]; then
    echo ""
    echo -e "${YELLOW}Note: ${INSTALL_DIR} is not in your PATH${NC}"
    echo ""
    echo "Add it to your shell config:"
    echo ""
    if [[ -f "$HOME/.zshrc" ]]; then
        echo "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.zshrc"
        echo "  source ~/.zshrc"
    elif [[ -f "$HOME/.bashrc" ]]; then
        echo "  echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.bashrc"
        echo "  source ~/.bashrc"
    else
        echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
    fi
    echo ""
fi

# Configure tmux for optimal agent-deck experience
configure_tmux() {
    local TMUX_CONF="$HOME/.tmux.conf"
    local MARKER="# agent-deck configuration"
    local VERSION_MARKER="# agent-deck-tmux-config-version:"
    local CURRENT_VERSION="2"  # Bump this when config changes
    local NEEDS_UPDATE=false
    local HAS_CONFIG=false

    # Check if already configured and if update is needed
    if [[ -f "$TMUX_CONF" ]] && grep -q "$MARKER" "$TMUX_CONF" 2>/dev/null; then
        HAS_CONFIG=true
        # Check version
        local INSTALLED_VERSION=$(grep "$VERSION_MARKER" "$TMUX_CONF" 2>/dev/null | sed "s/.*$VERSION_MARKER//" | tr -d ' ')
        if [[ -z "$INSTALLED_VERSION" || "$INSTALLED_VERSION" -lt "$CURRENT_VERSION" ]]; then
            NEEDS_UPDATE=true
            echo ""
            echo -e "${YELLOW}tmux config update available!${NC}"
            if [[ -z "$INSTALLED_VERSION" ]]; then
                echo "Your current agent-deck tmux config is from an older version."
            else
                echo "Installed version: $INSTALLED_VERSION, Available: $CURRENT_VERSION"
            fi
            echo ""
            echo -e "${BLUE}What's new in this update:${NC}"
            echo "  • Fixed mouse scrolling issues on WSL"
            echo "  • Added auto-enter copy-mode on scroll up"
            echo "  • Added explicit scroll bindings for copy-mode"
            echo "  • Improved terminal compatibility"
            echo ""
            read -p "Update tmux configuration? [Y/n] " -n 1 -r
            echo
            if [[ $REPLY =~ ^[Nn]$ ]]; then
                echo "Skipping tmux config update."
                return 0
            fi
            # Remove old config block
            echo "Removing old configuration..."
            # Use temp file for compatibility (BSD sed vs GNU sed)
            local TEMP_CONF=$(mktemp)
            sed "/$MARKER/,/# End agent-deck configuration/d" "$TMUX_CONF" > "$TEMP_CONF"
            mv "$TEMP_CONF" "$TMUX_CONF"
            echo -e "${GREEN}Old config removed${NC}"
        else
            echo -e "${GREEN}tmux already configured for agent-deck (v$INSTALLED_VERSION)${NC}"
            return 0
        fi
    fi

    echo ""
    echo -e "${BLUE}tmux Configuration${NC}"
    echo "Agent Deck works best with mouse scroll and clipboard support."
    echo ""

    if [[ -f "$TMUX_CONF" ]] && [[ "$NEEDS_UPDATE" != "true" ]]; then
        echo -e "Found existing config: ${YELLOW}~/.tmux.conf${NC}"
        echo "The following settings will be APPENDED (your existing config is preserved):"
    elif [[ "$NEEDS_UPDATE" == "true" ]]; then
        echo "Installing updated configuration..."
    else
        echo "No ~/.tmux.conf found. The following settings will be created:"
    fi

    echo ""
    echo -e "${BLUE}  • Mouse scrolling & drag-to-copy (WSL compatible)${NC}"
    echo -e "${BLUE}  • Auto copy-mode on scroll up${NC}"
    echo -e "${BLUE}  • Clipboard integration (copy to system clipboard)${NC}"
    echo -e "${BLUE}  • 256-color terminal support${NC}"
    echo -e "${BLUE}  • 10,000 line history${NC}"
    echo ""

    # Skip prompt if we're updating (user already confirmed)
    if [[ "$NEEDS_UPDATE" != "true" ]]; then
        read -p "Configure tmux for agent-deck? [Y/n] " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Nn]$ ]]; then
            echo "Skipping tmux configuration."
            echo "You can manually add the config later (see: agent-deck docs)"
            return 0
        fi
    fi

    # Determine clipboard command based on OS
    local CLIPBOARD_CMD
    if [[ "$OS" == "darwin" ]]; then
        CLIPBOARD_CMD="pbcopy"
    elif [[ "$IS_WSL" == "true" ]]; then
        # WSL: Use Windows clip.exe for clipboard integration
        CLIPBOARD_CMD="clip.exe"
        echo -e "${GREEN}WSL detected:${NC} Using Windows clipboard (clip.exe)"
    else
        # Linux - prefer xclip, fallback to xsel, or wl-copy for Wayland
        if [[ -n "$WAYLAND_DISPLAY" ]] && command -v wl-copy &> /dev/null; then
            CLIPBOARD_CMD="wl-copy"
        elif command -v xclip &> /dev/null; then
            CLIPBOARD_CMD="xclip -in -selection clipboard"
        elif command -v xsel &> /dev/null; then
            CLIPBOARD_CMD="xsel --clipboard --input"
        else
            echo -e "${YELLOW}Note: No clipboard tool found (xclip/xsel/wl-copy)${NC}"
            echo "Install with: sudo apt install xclip"
            CLIPBOARD_CMD="xclip -in -selection clipboard"
        fi
    fi

    # Create the config block
    # Note: WSL requires explicit scroll bindings; set-clipboard external doesn't work with clip.exe
    local CONFIG_BLOCK="
$MARKER
$VERSION_MARKER $CURRENT_VERSION
# Added by agent-deck installer - $(date +%Y-%m-%d)
# https://github.com/asheshgoplani/agent-deck

# Terminal with true color support
set -g default-terminal \"tmux-256color\"
set -ag terminal-overrides \",xterm*:Tc:smcup@:rmcup@\"
set -ag terminal-overrides \",*256col*:Tc\"

# Performance
set -sg escape-time 0
set -g history-limit 50000

# Mouse support (scroll + drag-to-copy)
set -g mouse on

# Auto-enter copy-mode when scrolling up (critical for WSL compatibility)
# This handles: 1) apps with mouse support, 2) already in copy-mode, 3) normal pane
bind-key -n WheelUpPane if-shell -F -t = \"#{mouse_any_flag}\" \"send-keys -M\" \"if -Ft= '#{pane_in_mode}' 'send-keys -M' 'copy-mode -e'\"

# Scroll bindings in copy-mode (both vi and emacs modes)
bind-key -T copy-mode-vi WheelUpPane send-keys -X scroll-up
bind-key -T copy-mode-vi WheelDownPane send-keys -X scroll-down
bind-key -T copy-mode WheelUpPane send-keys -X scroll-up
bind-key -T copy-mode WheelDownPane send-keys -X scroll-down

# Clipboard integration (drag-to-copy)
bind-key -T copy-mode-vi MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel \"$CLIPBOARD_CMD\"
bind-key -T copy-mode MouseDragEnd1Pane send-keys -X copy-pipe-and-cancel \"$CLIPBOARD_CMD\"
# End agent-deck configuration
"

    # Append to config file
    echo "$CONFIG_BLOCK" >> "$TMUX_CONF"

    echo -e "${GREEN}tmux configured successfully!${NC}"

    # Reload tmux config if tmux is running
    if tmux list-sessions &> /dev/null; then
        echo "Reloading tmux configuration..."
        tmux source-file "$TMUX_CONF" 2>/dev/null || true
        echo -e "${GREEN}tmux config reloaded${NC}"
    else
        echo "Run 'tmux source-file ~/.tmux.conf' to apply (or restart tmux)"
    fi
}

# Run tmux configuration (unless skipped)
if [[ "$SKIP_TMUX_CONFIG" != "true" ]]; then
    configure_tmux
else
    echo -e "${YELLOW}Skipping tmux configuration (--skip-tmux-config)${NC}"
fi

# Verify installation
if "$INSTALL_DIR/$BINARY_NAME" version &> /dev/null; then
    INSTALLED_VERSION=$("$INSTALL_DIR/$BINARY_NAME" version 2>&1 || echo "unknown")
    echo ""
    echo -e "${GREEN}╔════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║     Installation successful!           ║${NC}"
    echo -e "${GREEN}╚════════════════════════════════════════╝${NC}"
    echo ""
    echo -e "Version:  ${GREEN}${INSTALLED_VERSION}${NC}"
    echo -e "Binary:   ${GREEN}${INSTALL_DIR}/${BINARY_NAME}${NC}"
    echo -e "Platform: ${GREEN}${OS}/${ARCH}${NC}$([ "$IS_WSL" == "true" ] && echo -e " ${BLUE}(WSL)${NC}")"
    echo ""

    # Show dependency status
    echo "Dependencies:"
    if command -v tmux &> /dev/null; then
        echo -e "  ✓ tmux $(tmux -V 2>/dev/null | head -1)"
    else
        echo -e "  ${RED}✗ tmux (required - please install)${NC}"
    fi
    if command -v jq &> /dev/null; then
        echo -e "  ✓ jq $(jq --version 2>/dev/null)"
    else
        echo -e "  ${YELLOW}○ jq (optional - install for session forking)${NC}"
    fi
    echo ""

    # Show tmux config status
    if [[ -f "$HOME/.tmux.conf" ]] && grep -q "# agent-deck configuration" "$HOME/.tmux.conf" 2>/dev/null; then
        echo -e "tmux config: ${GREEN}Configured for mouse scroll + clipboard${NC}"
    else
        echo -e "tmux config: ${YELLOW}Not configured (run installer again or see docs)${NC}"
    fi
    echo ""

    echo "Get started:"
    echo "  ${BINARY_NAME}              # Launch the TUI"
    echo "  ${BINARY_NAME} add .        # Add current directory as session"
    echo "  ${BINARY_NAME} --help       # Show help"

    # WSL-specific tips
    if [[ "$IS_WSL" == "true" ]]; then
        echo ""
        echo -e "${BLUE}WSL Tips:${NC}"
        echo "  • Clipboard works with Windows (via clip.exe)"
        echo "  • Run in Windows Terminal for best experience"
        echo "  • Mouse scrolling works out of the box"
        echo ""
        # Check WSL version for socket pooling info
        if grep -qi "microsoft-standard" /proc/version 2>/dev/null; then
            echo -e "  ${GREEN}•${NC} WSL2 detected: MCP socket pooling supported"
        else
            echo -e "  ${YELLOW}•${NC} WSL1 detected: MCP socket pooling disabled"
            echo "    MCPs work fine in stdio mode (just uses more memory)"
            echo "    Upgrade to WSL2 for socket pooling: wsl --set-version <distro> 2"
        fi
    fi
else
    echo -e "${RED}Warning: Installation completed but verification failed${NC}"
    echo "The binary was installed but may not work correctly."
    echo ""
    echo "Troubleshooting:"
    echo "  1. Check if ${INSTALL_DIR} is in your PATH"
    echo "  2. Try: ${INSTALL_DIR}/${BINARY_NAME} version"
    echo "  3. If using zsh: source ~/.zshrc"
    echo "  4. If using bash: source ~/.bashrc"
fi
