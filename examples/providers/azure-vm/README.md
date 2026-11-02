# azure-vm provider

One small Azure Linux VM per sandbox, made and removed with the `az` CLI.
The VM comes up ready: Claude Code, git and tmux are installed, the project
folder is there, Claude's first-run screens are done, and (optionally) Claude
is logged in. The first session can start working at once.

## What it makes

Everything goes into one resource group you name. Per sandbox `adk-<10 hex>`:

| Resource | Name | Notes |
|----------|------|-------|
| Network security group | `<id>-nsg` | one inbound rule: TCP 22 from `ssh_source` only |
| Virtual network | `<id>-vnet` | 10.77.0.0/24, subnet `default` |
| Public IP | `<id>-ip` | Standard, static |
| Network interface | `<id>-nic` | |
| VM | `<id>` | `vm_size`, `image`, key-only ssh for `ssh_user` |
| OS disk | `<id>-osdisk` | StandardSSD, `os_disk_gb` |

Every one of them is tagged `agent-deck-provider=azure-vm` and
`agent-deck-sandbox=<id>`, plus `agent-deck-remote`, `agent-deck-profile` and
`agent-deck-create-id` from the request.

- `list` and `status` show only VMs that carry both tags.
- `destroy` deletes only resources that carry both tags, the VM first. It
  reads each resource's tags again right before deleting it. If a VM with the
  sandbox's name exists without those tags, it refuses and deletes nothing.
- A failed `create` deletes everything it tagged, then its local key.

### Why one shared resource group

The service principal gets Contributor on that one group and nothing else, so
even a bug in this script cannot touch anything outside it. A group per
sandbox would need rights to create groups in the subscription, which is far
wider. The tags keep sandboxes apart inside the group.

## Ready setup

1. cloud-init (custom-data, no secrets) installs git, tmux and curl if
   missing, Claude Code (Anthropic's native installer, saved to a file and run
   from there), and links `claude` and `agent-deck` into `/usr/local/bin`, so a
   non-login ssh command finds them.
2. The provider waits for cloud-init with `az vm run-command` and reads the
   VM's ed25519 host key the same way, through Azure's control plane. That key
   is returned in `host_keys`, so agent-deck pins it instead of trusting first
   use.
3. Over ssh it runs `agent-deck-sandbox-setup` with its input on stdin: the
   project (`project_git` clone, `project_dir` copy, or the built-in demo
   project at `~/demo`), Claude onboarding and project trust, and the
   optional login token. `sandbox-setup.sh` and `demo-project/` are copied
   from the docker example, so this example stands alone.

A cold create takes several minutes (most of it the VM boot and the Claude
Code download). Set `create_timeout_seconds` above `ready_timeout` so a slow
create can still roll back.

## Secrets

- **Service principal secret**: from `sp_secret_file` (a regular file you
  own, mode 0600 or 0400) or env `AGENTDECK_AZURE_SP_SECRET`. It is passed as
  `az login --service-principal --password @-` with the secret on stdin. The
  az CLI reads any argument value that is `@-` from stdin, so the secret is
  never in argv. The az CLI has no env-var login for a service principal.
- The login is made in a private `AZURE_CONFIG_DIR` inside the run's own
  0700 temp dir, which is removed when the run ends. Your own `~/.azure` is
  not used or changed. Without `sp_app_id`, the provider uses the `az login`
  you already have.
- **Claude token** (`claude_token_file`, from `claude setup-token`): copied in
  after boot over ssh, on stdin. Never in custom-data, tags, argv or logs.
  The file must be 0600 and hold exactly one line: the token (bare or as
  `CLAUDE_CODE_OAUTH_TOKEN=...`), no comments or blank lines. Anything else
  is refused before anything is made, and no error shows the file's content.
- Values from config never start with `@` or `-`, so az never reads a file or
  sees an option where a value belongs.

## ssh access

`ssh_source = auto` (the default) asks `public_ip_url` for your public IPv4
address and opens port 22 to that one address (`/32`). Or set an IP or CIDR,
for example your office range. Anything wider than a /8 (IPv4) or /32 (IPv6),
such as `0.0.0.0/0`, is refused.

Each sandbox gets a fresh ed25519 key in its own 0700 dir
`<state>/<id>.XXXXXXXX` (state: `AGENTDECK_AZURE_STATE_DIR`, default
`~/.local/state/agent-deck/azure-vm`). destroy removes it.

## Configure

`~/.config/agent-deck/config.toml`:

```toml
[providers.azure]
command = "/abs/path/to/agent-deck/examples/providers/azure-vm/provider"
create_timeout_seconds = 900
timeout_seconds = 600        # destroy deletes up to six resources one by one (VM ~1-3 min)
```

`~/.config/agent-deck/azure-vm.conf` (parsed, never sourced; see the top of
`provider` for every key):

```
resource_group = agent-deck-sandboxes
location       = westeurope
vm_size        = Standard_B2s
# ssh_source   = auto
# Service principal (else your own az login):
sp_app_id      = <app id>
sp_tenant      = <tenant id>
subscription   = <subscription id>
sp_secret_file = /home/you/.config/agent-deck/azure-sp-secret
# Optional:
# project_git       = https://github.com/you/repo.git
# claude_token_file = /home/you/.config/agent-deck/claude-token
# agent_deck_binary = /home/you/bin/agent-deck-linux-amd64
```

If your region or subscription does not offer `Standard_B2s`, pick another
small size from `az vm list-sizes -l <location> -o table` (for example
`Standard_B2als_v2`).

Needs on the machine that runs agent-deck: `az`, `ssh`, `ssh-keygen`,
`python3`, and `curl` for `ssh_source = auto`. On macOS, `brew install
coreutils` adds `gtimeout`, which bounds the cloud-init wait.

## Try it

```bash
agent-deck provider test azure --create     # create, status, destroy
agent-deck launch --provider azure -c claude /home/dev/demo
agent-deck remote destroy azure-<id>
```

Check that nothing is left:

```bash
az resource list -g agent-deck-sandboxes --tag agent-deck-provider=azure-vm -o table
```

## Cost

Standard_B2s (2 vCPU, 4 GiB) is a few US cents an hour, plus a little for the
static public IP and the 30 GB disk; check the Azure pricing page for your
region. A sandbox costs money until it is destroyed: destroy it after use.
