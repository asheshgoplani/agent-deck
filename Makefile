.PHONY: build run install clean dev release-local test fmt lint ci css tools css-verify

BINARY_NAME=agent-deck
BUILD_DIR=./build
VERSION=$(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//' || echo "dev")
LDFLAGS=-ldflags "-X main.Version=$(VERSION)"

# Tailwind v4 standalone CLI (PERF-01)
TAILWIND_VERSION=v4.2.2
TAILWIND_BIN=$(HOME)/.local/bin/tailwindcss

# Pin Go toolchain to 1.24.0 to prevent Go 1.25+ runtime regression on macOS
export GOTOOLCHAIN=go1.24.0

# Build the binary (requires compiled CSS via `make css`)
build: css
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/agent-deck

# Download the pinned Tailwind v4 standalone CLI binary if missing or wrong version
tools:
	@mkdir -p $(HOME)/.local/bin
	@OS=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	ARCH=$$(uname -m); \
	case "$$OS-$$ARCH" in \
		linux-x86_64)  ASSET=tailwindcss-linux-x64 ;; \
		linux-aarch64) ASSET=tailwindcss-linux-arm64 ;; \
		darwin-arm64)  ASSET=tailwindcss-macos-arm64 ;; \
		darwin-x86_64) ASSET=tailwindcss-macos-x64 ;; \
		*) echo "ERROR: unsupported host $$OS-$$ARCH for Tailwind binary" && exit 1 ;; \
	esac; \
	if [ -x "$(TAILWIND_BIN)" ] && "$(TAILWIND_BIN)" --help 2>&1 | grep -q "$(TAILWIND_VERSION)"; then \
		echo "tailwindcss $(TAILWIND_VERSION) already installed at $(TAILWIND_BIN)"; \
	else \
		echo "Downloading tailwindcss $(TAILWIND_VERSION) ($$ASSET) ..."; \
		curl -sSL -o "$(TAILWIND_BIN)" \
			"https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/$$ASSET"; \
		chmod +x "$(TAILWIND_BIN)"; \
		echo "Installed: $$( "$(TAILWIND_BIN)" --help 2>&1 | head -1 )"; \
	fi

# Compile Tailwind CSS (PERF-01 — replaces vendor/tailwind.js Play CDN runtime)
# Runs the brute-force diff gate (Pitfall #1 mitigation): generate twice and
# diff. Fails on any class present in brute-force output but missing from
# targeted globs unless explicitly allowlisted.
#
# Brute-force gate details:
#   - The brute src CSS must be semantically identical to styles.src.css
#     (same @theme, @variant dark, folded rules) so Tailwind can RECOGNISE
#     tn-* / sp-* / dark:* utilities. Without those, the diff is meaningless.
#   - Tailwind v4 resolves @source paths relative to the CSS source file's
#     directory, so the brute src MUST live inside internal/web/static/.
#     We copy styles.src.css to a hidden sibling and append a broader
#     @source glob. Any class the broader scan picks up that the targeted
#     scan missed will show up in the diff.
#   - The hidden copy is removed on success AND failure (trap).
css: tools
	@echo "==> Compiling Tailwind CSS (targeted globs)"
	$(TAILWIND_BIN) -i ./internal/web/static/styles.src.css \
		-o ./internal/web/static/styles.css \
		--minify
	@echo "==> Brute-force globbing diff (Pitfall #1 gate)"
	@trap 'rm -f ./internal/web/static/.brute-tw.src.css' EXIT INT TERM; \
	cp ./internal/web/static/styles.src.css ./internal/web/static/.brute-tw.src.css; \
	printf '\n/* --- brute-force additional @source (Pitfall #1 gate) --- */\n@source "./**/*.{js,mjs,html}";\n@source not "./vendor/**";\n@source not "./chart.umd.min.js";\n@source not "./sw.js";\n' \
		>> ./internal/web/static/.brute-tw.src.css; \
	$(TAILWIND_BIN) -i ./internal/web/static/.brute-tw.src.css \
		-o /tmp/agent-deck-tw-brute.css \
		--minify; \
	rm -f ./internal/web/static/.brute-tw.src.css; \
	if ! diff -q ./internal/web/static/styles.css /tmp/agent-deck-tw-brute.css >/dev/null 2>&1; then \
		if [ -f ./internal/web/static/.tailwind-allowlist.txt ]; then \
			echo "Brute-force diff non-empty but allowlist file present; review manually."; \
		else \
			echo "ERROR: brute-force @source diff non-empty (Pitfall #1). Add classes to internal/web/static/.tailwind-allowlist.txt or fix @source globs in styles.src.css." >&2; \
			diff ./internal/web/static/styles.css /tmp/agent-deck-tw-brute.css | head -40; \
			exit 1; \
		fi; \
	fi
	@SIZE=$$(gzip -c ./internal/web/static/styles.css | wc -c); \
	echo "==> Compiled styles.css gzipped size: $$SIZE bytes"; \
	if [ "$$SIZE" -gt 10240 ]; then \
		echo "ERROR: gzipped size $$SIZE exceeds 10240-byte sanity ceiling"; \
		exit 1; \
	fi

# Drift gate: regenerate CSS and fail if committed file differs from generated
css-verify: css
	@git diff --exit-code -- ./internal/web/static/styles.css || \
		(echo "ERROR: internal/web/static/styles.css drifted from generated output. Run 'make css' and commit." && exit 1)

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
	@which air > /dev/null || go install github.com/air-verse/air@latest
	$(shell go env GOPATH)/bin/air

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
	CODE_VERSION=$$(grep 'var Version' cmd/agent-deck/main.go | sed 's/.*"\(.*\)".*/\1/'); \
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
