FROM golang:1.24

# System dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    tmux \
    jq \
    make \
    curl \
    git \
    && rm -rf /var/lib/apt/lists/*

# Go dev tools
RUN go install github.com/air-verse/air@latest \
    && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest \
    && go install golang.org/x/tools/gopls@latest

# Lefthook (git hooks manager)
RUN go install github.com/evilmartians/lefthook@latest

# Non-root user (standard devcontainer convention)
ARG USERNAME=vscode
ARG USER_UID=1000
ARG USER_GID=$USER_UID
RUN groupadd --gid $USER_GID $USERNAME \
    && useradd --uid $USER_UID --gid $USER_GID -m $USERNAME \
    && mkdir -p /go/pkg /go/bin \
    && chown -R $USERNAME:$USERNAME /go

USER $USERNAME
