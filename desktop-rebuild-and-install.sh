#!/bin/bash
# Rebuild and install RevDen desktop app (production) from source
# This temporarily swaps wails.json to production config for the build
set -e

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DESKTOP_DIR="$REPO_DIR/cmd/agent-deck-desktop"
BUILD_DIR="$DESKTOP_DIR/build/bin"
WAILS_JSON="$DESKTOP_DIR/wails.json"
APP_NAME="RevDen.app"

# Production wails.json config
PROD_CONFIG='{
  "$schema": "https://wails.io/schemas/config.v2.json",
  "name": "RevDen",
  "outputfilename": "RevDen",
  "frontend:install": "npm install",
  "frontend:build": "npm run build",
  "frontend:dev:watcher": "npm run dev",
  "frontend:dev:serverUrl": "auto",
  "author": {
    "name": "Jason Cumberland",
    "email": "jason.cumberland@revenium.io"
  },
  "info": {
    "productName": "RevDen",
    "companyName": "Revenium",
    "copyright": "Copyright © 2025 Revenium",
    "comments": "AI Agent Session Manager"
  }
}'

# Backup current wails.json (likely dev config)
cp "$WAILS_JSON" "$WAILS_JSON.dev.bak"

# Restore dev config on exit (success or failure)
cleanup() {
    if [ -f "$WAILS_JSON.dev.bak" ]; then
        mv "$WAILS_JSON.dev.bak" "$WAILS_JSON"
    fi
}
trap cleanup EXIT

# Write production config
echo "$PROD_CONFIG" > "$WAILS_JSON"

echo "Building RevDen desktop app (production)..."
cd "$REPO_DIR"
make desktop-build

# Verify the build succeeded
if [ ! -d "$BUILD_DIR/$APP_NAME" ]; then
    echo "Error: Build failed - $APP_NAME not found in $BUILD_DIR"
    ls -la "$BUILD_DIR" 2>/dev/null || echo "(directory does not exist)"
    exit 1
fi

echo ""
echo "Installing $APP_NAME to /Applications..."
rm -rf "/Applications/$APP_NAME"
cp -r "$BUILD_DIR/$APP_NAME" "/Applications/"

echo "✅ Done! $APP_NAME installed to /Applications"
