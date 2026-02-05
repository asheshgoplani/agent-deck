.PHONY: build run install clean dev release-local test fmt lint ci

BINARY_NAME=agent-deck
BUILD_DIR=./build
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.Version=$(VERSION)"

# Build the binary
build:
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/agent-deck

# Run in development
run:
	go run ./cmd/agent-deck

# Install to /usr/local/bin (requires sudo)
install: build
	sudo cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)
	@echo "✅ Installed to /usr/local/bin/$(BINARY_NAME)"
	@echo "Run 'agent-deck' to start"

# Install to user's local bin (no sudo required)
install-user: build
	mkdir -p $(HOME)/.local/bin
	cp $(BUILD_DIR)/$(BINARY_NAME) $(HOME)/.local/bin/$(BINARY_NAME)
	@echo "✅ Installed to $(HOME)/.local/bin/$(BINARY_NAME)"
	@echo "Make sure $(HOME)/.local/bin is in your PATH"
	@echo "Run 'agent-deck' to start"

# Uninstall from /usr/local/bin
uninstall:
	sudo rm -f /usr/local/bin/$(BINARY_NAME)
	@echo "✅ Uninstalled $(BINARY_NAME)"

# Uninstall from user's local bin
uninstall-user:
	rm -f $(HOME)/.local/bin/$(BINARY_NAME)
	@echo "✅ Uninstalled $(BINARY_NAME)"

# Clean build artifacts
clean:
	rm -rf $(BUILD_DIR)
	go clean

# Development with auto-reload
dev:
	@which air > /dev/null || go install github.com/cosmtrek/air@latest
	air

# Run tests (with race detector)
test:
	go test -race -v ./...

# Format code
fmt:
	go fmt ./...

# Lint
lint:
	@which golangci-lint > /dev/null || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	golangci-lint run

# Run local CI checks (same as pre-push hook: lint + test + build in parallel)
ci:
	@which lefthook > /dev/null || (echo "ERROR: lefthook not found. Run: brew install lefthook" && exit 1)
	lefthook run pre-push --force --no-auto-install

# Local release using GoReleaser
# Prerequisites: brew install goreleaser
# Required env: GITHUB_TOKEN, HOMEBREW_TAP_GITHUB_TOKEN
release-local:
	@echo "=== Pre-flight checks ==="
	@which goreleaser > /dev/null || (echo "ERROR: goreleaser not found. Run: brew install goreleaser" && exit 1)
	@test -n "$$GITHUB_TOKEN" || (echo "ERROR: GITHUB_TOKEN not set" && exit 1)
	@test -n "$$HOMEBREW_TAP_GITHUB_TOKEN" || (echo "ERROR: HOMEBREW_TAP_GITHUB_TOKEN not set" && exit 1)
	@TAG=$$(git describe --tags --exact-match 2>/dev/null) || (echo "ERROR: HEAD is not tagged. Run: git tag vX.Y.Z" && exit 1); \
	CODE_VERSION=$$(grep 'const Version' cmd/agent-deck/main.go | sed 's/.*"\(.*\)".*/\1/'); \
	TAG_VERSION=$${TAG#v}; \
	if [ "$$TAG_VERSION" != "$$CODE_VERSION" ]; then \
		echo "ERROR: Tag $$TAG ($$TAG_VERSION) != code Version $$CODE_VERSION"; \
		exit 1; \
	fi; \
	echo "Version: $$CODE_VERSION"
	@echo "=== Running tests ==="
	go test -race ./...
	@echo "=== Running GoReleaser ==="
	goreleaser release --clean
	@echo "=== Release complete ==="
	@echo "Verify: gh release view $$(git describe --tags --exact-match) --repo asheshgoplani/agent-deck"
