package session

import _ "embed"

// conductorBridgePy is the Python bridge script that connects Telegram, Slack,
// and/or Discord to conductor sessions. It is embedded so the binary is
// self-contained: InstallBridgeScript / update.UpdateBridgePy write these exact
// bytes to <data>/conductor/bridge.py at setup/update time.
//
// There is EXACTLY ONE canonical copy of this script in the repo —
// internal/session/conductor_bridge.py, embedded directly below. No mirror and
// no go:generate sync step: the embedded bytes ARE the canonical file, so they
// cannot drift. conductor/tests load this same file (see conductor/tests/
// conftest.py), and conductor/README points here for discoverability.
//
//go:embed conductor_bridge.py
var conductorBridgePy string
