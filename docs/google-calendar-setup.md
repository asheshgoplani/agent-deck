# Google Calendar Integration Setup

Agent-deck can show your upcoming meetings in the tmux status bar and include meeting
proximity in conductor heartbeat messages. This guide walks through the one-time setup.

## Prerequisites

- A Google account with Google Calendar
- Access to [Google Cloud Console](https://console.cloud.google.com)
- `agent-deck google-calendar auth` run once to authorize

---

## Step 1: Create a Google Cloud Project and OAuth Credentials

1. Go to [Google Cloud Console](https://console.cloud.google.com) and create a new project
   (or select an existing one).

2. Enable the **Google Calendar API**:
   - Navigate to **APIs & Services → Library**
   - Search for "Google Calendar API" and click **Enable**

3. Create OAuth 2.0 credentials:
   - Navigate to **APIs & Services → Credentials**
   - Click **Create Credentials → OAuth client ID**
   - Application type: **Desktop app**
   - Give it a name (e.g., "agent-deck") and click **Create**

4. Download the credentials:
   - Click the download icon next to your new OAuth client
   - Save the file as `credentials.json`

5. If prompted, configure the OAuth consent screen:
   - User type: **External** (or Internal if your org allows it)
   - Add your own email address as a test user
   - Scopes: add `https://www.googleapis.com/auth/calendar.readonly`

---

## Step 2: Place credentials.json

Move the downloaded file to the agent-deck data directory:

```bash
mkdir -p ~/.agent-deck
mv ~/Downloads/credentials.json ~/.agent-deck/google-calendar-credentials.json
chmod 600 ~/.agent-deck/google-calendar-credentials.json
```

The default path is `~/.agent-deck/google-calendar-credentials.json`. You can override
this with `credentials_path` in your config (see Step 3).

---

## Step 3: Add `[google_calendar]` to config.toml

Open `~/.agent-deck/config.toml` (create it if it doesn't exist) and add:

```toml
[google_calendar]
enabled = true

# Optional: list specific calendars to include (default: primary only)
# calendar_ids = ["primary", "work@yourcompany.com"]

# Optional: how far ahead to look for events (default: "2h")
# lookahead = "2h"

# Optional: how often to poll the API (default: "60s")
# poll_interval = "60s"

# Optional: custom credentials path (default: ~/.agent-deck/google-calendar-credentials.json)
# credentials_path = "/path/to/credentials.json"
```

---

## Step 4: Authorize agent-deck

Run the auth command, which opens your browser for the OAuth consent flow:

```bash
agent-deck google-calendar auth
```

This opens a browser tab asking you to authorize agent-deck's read-only access to your
calendar. After approving, the page shows "Authorization successful!" and the OAuth token
is saved to `~/.agent-deck/google-calendar-token.json`.

You only need to do this once. The token is refreshed automatically when it expires.

---

## Step 5: Verify the integration

Fetch upcoming events to confirm everything is working:

```bash
agent-deck google-calendar test
```

Expected output:

```
Found 3 upcoming events:

  12m  Standup (video)
  1h45m  Sprint Planning (video)
  2h30m  1:1 with Manager
```

If the output shows "No upcoming events found", check that `enabled = true` is set and
that the configured calendars have events in the next 2 hours.

---

## Troubleshooting

**`Error: open credentials.json: no such file or directory`**
The credentials file is missing or at the wrong path. Verify:
```bash
ls -la ~/.agent-deck/google-calendar-credentials.json
```
If you used `credentials_path` in config.toml, double-check that path.

**`no token found — run 'agent-deck google-calendar auth' first`**
The OAuth token hasn't been created yet. Run:
```bash
agent-deck google-calendar auth
```

**`HTTP 401` or token-related errors**
The token has expired or been revoked. Re-authorize:
```bash
rm ~/.agent-deck/google-calendar-token.json
agent-deck google-calendar auth
```

**Wrong scopes / `insufficientPermissions`**
The OAuth client was authorized without the calendar scope. Delete the token and
re-authorize — the auth flow will request the correct `calendar.readonly` scope.

**Events not showing in the tmux bar**
- Confirm `enabled = true` in `[google_calendar]` config
- Confirm the `lookahead` window covers when your next meeting starts
- Run `agent-deck google-calendar test` to verify the collector fetches events
- Check `~/.agent-deck/debug.log` with `AGENTDECK_DEBUG=1 agent-deck` for calendar errors

**All-day events are not shown**
This is by design. The integration only surfaces timed events with a specific start time,
since all-day events do not have meaningful meeting-proximity urgency.
