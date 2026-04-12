# Agent Deck Sandbox Image
# Provides a containerised environment with AI coding CLIs.
#
# Build:  docker build -t agent-deck-sandbox sandbox/
# Test:   docker run --rm -it agent-deck-sandbox bash

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# Core tooling needed by the AI CLIs and typical dev workflows.
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    curl \
    fzf \
    git \
    git-lfs \
    gnupg \
    jq \
    openssh-client \
    python3 \
    python3-pip \
    ripgrep \
    unzip \
    uuid-runtime \
    vim-tiny \
    wget \
  && rm -rf /var/lib/apt/lists/*

# Set up Git LFS hooks globally.
RUN git lfs install

# Install Node.js 22.x LTS (required for Codex and Gemini CLIs).
# Pin to 22.x series; patch updates come from nodesource.
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
  && apt-get install -y nodejs \
  && rm -rf /var/lib/apt/lists/*

# The container runs as the host user (--user uid:gid) at runtime, not root.
# Tools are installed under /root at build time; chmod 755 makes them traversable.
# Security: --cap-drop=ALL, --read-only, and --security-opt=no-new-privileges
# are enforced at runtime. IS_SANDBOX=1 gates --dangerously-skip-permissions.
WORKDIR /root

# ── Claude Code (native installer) ──────────────────────────────────
RUN curl -fsSL https://claude.ai/install.sh | bash
ENV PATH="/root/.local/bin:${PATH}"

# ── OpenCode (native installer) ─────────────────────────────────────
RUN curl -fsSL https://opencode.ai/install | bash
ENV PATH="/root/.opencode/bin:${PATH}"

# ── Codex CLI (OpenAI) ──────────────────────────────────────────────
# Pin major version; update periodically.
RUN npm install -g @openai/codex@0

# ── Gemini CLI (Google) ─────────────────────────────────────────────
# Pin major version; update periodically.
RUN npm install -g @google/gemini-cli@0

# Pre-create credential directories for auth volume mounts.
RUN mkdir -p /root/.claude \
    /root/.config/opencode \
    /root/.local/share/opencode \
    /root/.local/state/opencode \
    /root/.codex \
    /root/.gemini \
    /root/.ssh

# Make /root traversable by the non-root runtime user (--user uid:gid).
# Installed binaries at /root/.local/bin/ and /root/.opencode/bin/ need
# the parent directory to have the execute bit for path resolution.
RUN chmod 755 /root

# Allow Claude Code to use --dangerously-skip-permissions in sandbox.
ENV IS_SANDBOX=1

# Workspace directory (host project is bind-mounted here).
WORKDIR /workspace

# Keep the container alive for docker exec.
CMD ["sleep", "infinity"]
