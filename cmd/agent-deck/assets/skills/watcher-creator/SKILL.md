---
name: watcher-creator
description: Guide for creating agent-deck watchers conversationally. This skill should be used when users want to set up a new watcher (webhook, ntfy, github, slack, gmail) to route events to a conductor. It walks the user through selecting an adapter type, gathering required settings, generating watcher.toml and clients.json entries, and emits the exact `agent-deck watcher create` command to run.
---

# Watcher Creator

## Overview

This skill guides users through setting up a new agent-deck watcher from scratch. A watcher listens for incoming events (webhooks, ntfy notifications, GitHub events, Slack messages, or Gmail messages) and routes them to the appropriate conductor session. The skill asks clarifying questions, collects required settings, and emits the exact CLI command and configuration files to create a working watcher.

## When to use

Invoke this skill when a user says things like:
- "I want to set up a new watcher"
- "How do I connect GitHub webhooks to agent-deck?"
- "Help me route Slack messages to a conductor"
- "Create a watcher for my Gmail inbox"
- "I need agent-deck to listen for ntfy notifications"

## Step-by-step conversational flow

### Step 1: Choose adapter type

Ask the user which source they want to receive events from. Present the five options:

1. **webhook** - Generic HTTP POST webhook (any service that can send HTTP)
2. **ntfy** - ntfy.sh push notification topic
3. **github** - GitHub repository webhooks (issues, PRs, pushes)
4. **slack** - Slack messages via a Cloudflare Worker bridge to ntfy
5. **gmail** - Gmail inbox via Google Cloud Pub/Sub watch

### Step 2: Gather required settings for the chosen type

Once the user picks a type, ask for the settings specific to that adapter (see the per-type sections below). Ask for each required setting one at a time to avoid overwhelming the user.

Also ask:
- **Watcher name**: A short lowercase identifier (e.g., `my-webhook`, `github-alerts`). This becomes the directory name under the effective watcher data dir (`$XDG_DATA_HOME/agent-deck/watcher` for new users; legacy `~/.agent-deck/watcher` remains in use when existing watcher state is present).
- **Conductor**: Which conductor session this watcher should route events to by default (the `conductor` field in `clients.json`).
- **Group**: Which group the conductor session lives in (the `group` field in `clients.json`).

### Step 3: Generate a watcher.toml block

Produce a TOML configuration block for the user to save as `$XDG_DATA_HOME/agent-deck/watcher/<name>/watcher.toml`, or the legacy `~/.agent-deck/watcher/<name>/watcher.toml` if agent-deck is already using legacy watcher state.

Example for webhook:
```toml
[watcher]
name = "my-webhook"
type = "webhook"

[adapter]
port = "18460"
bind = "127.0.0.1"

[routing]
conductor = "main-conductor"
group = "inbox"
```

### Step 4: Generate a clients.json entry

Produce the JSON entry keyed by the expected sender. The key format depends on adapter type:

- webhook: the `X-Webhook-Sender` header value the sender will use (e.g., an IP or a service name)
- ntfy: `ntfy:<topic>@<server-host>` (e.g., `ntfy:my-topic@ntfy.sh`)
- github: `<github-username>@github.com`
- slack: `slack:<CHANNEL_ID>` (e.g., `slack:C0AABSF5GKD`)
- gmail: the sender's email address (e.g., `user@example.com`)

Entry format:
```json
{
  "sender-key": {
    "conductor": "main-conductor",
    "group": "inbox",
    "name": "Human-readable label"
  }
}
```

### Step 5: Emit the exact agent-deck watcher create command

Show the user the full CLI command to run. Only emit commands — do not execute them.

## Supported watcher types and required fields

### webhook

The webhook adapter runs a local HTTP server. Any service that can POST to an HTTP endpoint can deliver events.

**Required settings:**
- `port` (string): TCP port to listen on. Default is `"18460"`. Must be available on the local machine.

**Optional settings:**
- `bind` (string): Bind address. Default is `"127.0.0.1"` (loopback only, per security policy T-14-04). Use `"0.0.0.0"` only if the sender is remote and you understand the exposure.

**Sender key in clients.json:** Set the `X-Webhook-Sender` header from the sending service, or use the remote IP if the header is absent.

**Create command:**
```
agent-deck watcher create webhook --name <name> --port <port>
```

### ntfy

The ntfy adapter subscribes to an ntfy.sh topic via NDJSON streaming. It auto-reconnects with exponential backoff.

**Required settings:**
- `topic` (string): The ntfy topic name (e.g., `"my-alerts"`).

**Optional settings:**
- `server` (string): ntfy server URL. Default is `"https://ntfy.sh"`.

**Sender key in clients.json:** `ntfy:<topic>@<server-host>` (e.g., `ntfy:my-alerts@ntfy.sh`).

**Create command:**
```
agent-deck watcher create ntfy --name <name> --topic <topic>
```

### github

The GitHub adapter runs a local HTTP server that receives GitHub webhook POST requests and verifies HMAC-SHA256 signatures.

**Required settings:**
- `secret` (string): The webhook secret configured in GitHub (Settings > Webhooks > Secret). Used for HMAC-SHA256 signature verification.

**Optional settings:**
- `port` (string): TCP port to listen on. Default is `"18461"`.
- `bind` (string): Bind address. Default is `"127.0.0.1"`. Use a public address or set up a tunnel (e.g., ngrok) if GitHub must reach this machine.

**Supported event types:** issues, pull_request, push. Unknown event types produce a generic event.

**Sender key in clients.json:** `<github-username>@github.com` (e.g., `octocat@github.com`).

**Create command:**
```
agent-deck watcher create github --name <name> --secret <secret>
```

### slack

The Slack adapter subscribes to an ntfy topic that receives bridged Slack events from a Cloudflare Worker. It supports both v1 (plain text) and v2 (structured JSON) payloads.

**Required settings:**
- `topic` (string): The ntfy topic that the Cloudflare Worker posts Slack events to.

**Optional settings:**
- `server` (string): ntfy server URL. Default is `"https://ntfy.sh"`.

**Sender key in clients.json:** `slack:<CHANNEL_ID>` (e.g., `slack:C0AABSF5GKD`). The channel ID comes from the Slack API or the channel URL in the Slack app.

**Create command:**
```
agent-deck watcher create slack --name <name> --topic <topic>
```

### gmail

The Gmail adapter delivers normalized events from a Gmail account via Google Cloud Pub/Sub. It requires initial OAuth setup and a Pub/Sub topic + subscription.

**Required settings:**
- `topic` (string): Full Pub/Sub topic resource name: `projects/<project-id>/topics/<topic-name>`.
- `subscription` (string): Full Pub/Sub subscription resource name: `projects/<project-id>/subscriptions/<sub-name>`.

**Optional settings:**
- `credentials_path` (string): Path to the OAuth client credentials JSON file (downloaded from Google Cloud Console). Default: `<effective watcher data dir>/<name>/credentials.json`.
- `token_path` (string): Path to the persisted OAuth token JSON. Default: `<effective watcher data dir>/<name>/token.json`.
- `labels` (string): Comma-separated Gmail label filter (e.g., `"INBOX,UNREAD"`). If absent, all messages pass through.
- `account` (string): Informational Gmail address for display purposes.

**Pre-requisites before running agent-deck watcher create:**
1. Create a Google Cloud project and enable the Gmail API and Cloud Pub/Sub API.
2. Create a Pub/Sub topic and subscription.
3. Grant the Gmail service account (`gmail-api-push@system.gserviceaccount.com`) the `Pub/Sub Publisher` role on the topic.
4. Download OAuth client credentials (Desktop app type) to `credentials.json`.
5. Run `agent-deck watcher oauth login` (Phase 19+) or manually place a `token.json` in the watcher directory.

**Sender key in clients.json:** The sender's email address (e.g., `contact@example.com`).

**Create command:**
```
agent-deck watcher create gmail --name <name>
```

## Validation checklist

Before emitting the final `agent-deck watcher create` command, confirm:

1. **Name is valid:** Lowercase letters, numbers, and hyphens only. No spaces or special characters.
2. **Port is available (webhook/github):** The chosen port is not already in use by another watcher or service.
3. **Conductor session exists:** The conductor name matches an active agent-deck session (check with `agent-deck session list`).
4. **Required settings collected:** All required fields for the chosen adapter type are present and non-empty.
5. **clients.json entry is correct:** The sender key format matches the adapter's normalization (e.g., `slack:CHANNEL_ID` not just `CHANNEL_ID`).

## Example walkthrough

**User:** I want to set up a watcher for GitHub webhook events on my repo.

**Skill response:**
Great, let me help you set up a GitHub watcher.

1. What name should this watcher have? (e.g., `github-myrepo`)
2. What port should it listen on? (default: 18461)
3. What webhook secret will you use? (you'll configure the same value in GitHub Settings > Webhooks)
4. Which conductor session should receive the events? (e.g., `main-conductor`)
5. Which group is that conductor in? (e.g., `dev`)

**User:** Name: github-myrepo, port: 18461, secret: mysecret, conductor: main-conductor, group: dev

**Skill response:**
Here is your configuration.

Save this as `$XDG_DATA_HOME/agent-deck/watcher/github-myrepo/watcher.toml` for a new install, or under legacy `~/.agent-deck/watcher/github-myrepo/watcher.toml` if agent-deck is already using legacy watcher state:
```toml
[watcher]
name = "github-myrepo"
type = "github"

[adapter]
secret = "mysecret"
port = "18461"
bind = "127.0.0.1"

[routing]
conductor = "main-conductor"
group = "dev"
```

Add this entry to `$XDG_DATA_HOME/agent-deck/watcher/clients.json` (key is `<your-github-username>@github.com`), or the legacy watcher `clients.json` if that is the effective watcher data dir:
```json
{
  "octocat@github.com": {
    "conductor": "main-conductor",
    "group": "dev",
    "name": "My GitHub Repo"
  }
}
```

Now run:
```
agent-deck watcher create github --name github-myrepo --secret mysecret
```

Then start the watcher:
```
agent-deck watcher start github-myrepo
```

Configure GitHub to send webhooks to `http://<your-server>:18461/github`.

## Notes

This skill is read-only (D-22): it guides you through configuration and emits commands to run, but it does NOT execute `agent-deck watcher create` itself. Copy and run the commands shown above in your terminal.

All five adapter types (webhook, ntfy, github, slack, gmail) route incoming events through the same engine. Events are matched against `<effective watcher data dir>/clients.json` by sender. Unknown senders are sent to triage in Phase 18+.

Install this skill to your pool with:
```
agent-deck watcher install-skill watcher-creator
```
