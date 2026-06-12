package session

import _ "embed"

// conductorBridgePy is the Python bridge script that connects Telegram, Slack,
// and/or Discord to conductor sessions. It is embedded so the binary is
// self-contained (InstallBridgeScript / update.UpdateBridgePy write these exact
// bytes to <data>/conductor/bridge.py).
//
// Single source of truth: the canonical script lives at conductor/bridge.py
// (imported directly by conductor/tests). conductor_bridge.py in this package
// is a byte-identical mirror kept in sync by `go generate` and pinned by
// TestEmbeddedBridgeMatchesCanonical, so the embedded/deployed bytes can never
// silently drift from the tested file.
//
//go:generate cp ../../conductor/bridge.py conductor_bridge.py
//go:embed conductor_bridge.py
var conductorBridgePy string
