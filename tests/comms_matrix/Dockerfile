# Build the safe tooling stage from sandbox/Dockerfile first (see run.sh).
ARG COMMS_BASE_IMAGE=agent-deck-comms-base:local
FROM golang:1.25.13-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /agent-deck ./cmd/agent-deck

FROM ${COMMS_BASE_IMAGE}
RUN apt-get update && apt-get install -y --no-install-recommends tmux openssh-server \
    && python3 -c "import shutil; shutil.rmtree('/var/lib/apt/lists')"
COPY --from=build /agent-deck /usr/local/bin/agent-deck
COPY . /workspace
RUN for tool in claude codex gemini pi hermes opencode cursor cursor-agent agent; do ln -s "/workspace/tests/comms_matrix/bin/$tool" "/usr/local/bin/$tool"; done
RUN mkdir -p /run/sshd /rig-keys /artifacts /root/.ssh \
    && printf '%s\n' 'Host r1' '  User root' '  IdentityFile /rig-keys/id_ed25519' '  UserKnownHostsFile /rig-keys/known_hosts' '  StrictHostKeyChecking yes' '  BatchMode yes' '  IdentitiesOnly yes' > /etc/ssh/ssh_config.d/comms.conf
ENV PATH="/workspace/tests/comms_matrix/bin:${PATH}" \
    AGENTDECK_SKIP_UPDATE_CHECK=1 CI=true COMMS_REMOTE=r1
WORKDIR /workspace
CMD ["python3", "/workspace/tests/comms_matrix/runner.py", "--output", "/artifacts"]
