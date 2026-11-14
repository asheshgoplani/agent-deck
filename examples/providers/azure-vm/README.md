# azure-vm provider

One small Azure Linux VM per sandbox, made and removed with the `az` CLI.
The VM comes up ready: Claude Code, git and tmux are installed, the project
folder is there, Claude's first-run screens are done, and (optionally) Claude
is logged in. The first session can start working at once.

## Connect in one step

```bash
agent-deck provider connect azure
```

This sets up everything the [manual setup](#manual-setup) below does. You
need only the `az` CLI and an Azure subscription. It works over ssh.

### What it does

1. Checks that `az` is installed. If not, it prints the install line for your
   OS and stops.
2. Uses your `az` login if you have one (`Using your az login: <you>`).
   Otherwise it logs in with a one-time code: it prints a link and a code.
   Open the link on any phone or computer and enter the code.
3. Lists your subscriptions. With one, it uses it. With several, it asks you
   to pick one (or use `--subscription`). It never changes your default
   `az` subscription.
4. Enables `Microsoft.Compute` and `Microsoft.Network` on the subscription
   if they are not on yet (new subscriptions often miss them).
5. Looks for the resource group (default `agent-deck-sandboxes`). An
   existing group is used as is, in its own region.
6. Checks that the region offers the VM size to your subscription
   (`az vm list-skus`; this call can take a minute or two). Without
   `--vm-size` (and no `vm_size` in `azure-vm.conf`) it picks the first
   offered of `Standard_B2s`, `Standard_B2als_v2`, `Standard_B2as_v2`,
   `Standard_B2s_v2`, `Standard_D2as_v5`, `Standard_D2s_v5` (all 2 vCPUs)
   and says so.
7. Checks the vCPU quota in that region for one VM of that size. This
   comes before the group is created, so a full region leaves nothing behind.
8. Creates the resource group if it is missing (tagged `agent-deck=sandboxes`).
9. Tests that the region takes new resources: it creates a free network
   security group `agent-deck-region-probe` (tagged `agent-deck=probe`) in
   the group and deletes it again. Some regions refuse new customers on some
   subscriptions. Without `--location` it then tries `swedencentral`,
   `northeurope`, `germanywestcentral`, `francecentral`, `uksouth`, `eastus2`
   in that order, writes the first that works as `location` in
   `azure-vm.conf` and says so. The group stays where it is: the VMs can
   live in another region than their group. A re-run that keeps a working
   stored setup skips steps 6 and 9.
10. Creates a service principal (a robot login for agent-deck) with
   Contributor on that one resource group and nothing else. It warns if the
   robot login has roles anywhere else. If a service principal with that
   name already exists and this machine did not set it up, it stops: reusing
   it would give it a new secret and revoke the one other machines use. Pick
   another `--sp-name`, or pass `--reset-secret` to take it over on purpose.
11. Stores the secret, `azure-vm.conf`, a copy of this provider, and the
   `[providers.azure]` entry in `config.toml` (see [Where files go](#where-files-go)).
12. Checks the result: it lists the VMs in the group, logged in as the robot
   login. A new service principal can take a few minutes to show up in
   Azure, so it retries for up to 3 minutes.

When it is done it prints:

```
Azure is connected.
  Subscription:    <name> (<id>)
  Resource group:  agent-deck-sandboxes (westeurope)
  Robot login:     <app id> (Contributor on that group only)
  Secret:          ~/.config/agent-deck/azure-sp-secret (0600, never shown)
  Settings:        ~/.config/agent-deck/azure-vm.conf
Check passed: the robot login sees resource group agent-deck-sandboxes (0 sandbox(es) now).
Next: agent-deck launch --provider azure -c claude <project path>
```

### Flags

| Flag | Default | What it does |
|------|---------|--------------|
| `--subscription` | pick | Subscription id or exact name |
| `--tenant` | your account's own | Directory (tenant) id or domain to log in to |
| `--resource-group` | `agent-deck-sandboxes` | The one group sandboxes live in |
| `--location` | `westeurope` | Region for a new group and its VMs (an existing group keeps its own); when given, a region that refuses new resources is an error instead of a fallback |
| `--vm-size` | `Standard_B2s` | VM size: checked in the region, used for the quota check and written to `azure-vm.conf`. Without it a `vm_size` already there is kept, and a missing one is set to the first offered small size |
| `--sp-name` | `agent-deck-sandboxes-sp` | Service principal display name |
| `--reset-secret` | off | Issue a new secret even when a working one is stored |
| `--sp-app-id` | | Use a service principal someone else made (with `--sp-secret-stdin`) |
| `--sp-secret-stdin` | off | Read that service principal's secret from stdin, one line |
| `--provider-command` | bundled copy | Use this provider executable instead |
| `--name` | `azure` | The `[providers.<name>]` entry to write |
| `--disconnect` | off | Undo: remove the service principal and the local files |
| `--yes` | off | Do not ask before disconnecting |
| `--force` | off | Disconnect even when sandboxes still exist |
| `--json` | off | One JSON event per line on stdout (for the Mac app) |

Exit codes: 0 done (or already connected), 1 error, 2 usage, 3 a choice is
needed (several subscriptions, no terminal, no `--subscription`).

With `--json` the last event is `{"event":"done", ..., "changed": true|false}`;
`changed` is false when the run found everything already in place.

On Windows the bundled provider (a bash script) cannot run. Run agent-deck
inside WSL and connect there, or pass `--provider-command`.

### Run it again any time

Re-running is safe. It finds the group and the stored robot login and keeps
them (`Service principal <app id> is already set up; keeping its secret.`).
If nothing changed it prints `Azure is already connected (nothing changed).`
(and no `Saved the settings in ...` line).
Your own lines in `azure-vm.conf` (comments, `project_git`, ...) are kept;
only its managed keys are updated. `--reset-secret` issues a new secret.

If `[providers.azure]` already points at another provider command, it is
kept (`Keeping your [providers.azure] command: <path>`). Pass
`--provider-command` to change it.

### Disconnect

```bash
agent-deck provider connect azure --disconnect   # or: agent-deck provider disconnect azure
```

It asks first, before it calls Azure at all (`--yes` skips the question;
without a terminal `--yes` is required). It refuses while sandboxes still
exist; destroy them first with `agent-deck remote destroy <name>`, or add
`--force`. Then it removes the service principal's role and the service
principal from Azure, but only one that `connect` created (`azure-vm.conf`
records that as `sp_created = true`). A service principal you got with
`--sp-app-id`, or took over with `--reset-secret`, belongs to its owner and
stays in Azure. Locally it removes the secret file, the managed keys in
`azure-vm.conf`, the `[providers.azure]` entry if it points at the bundled
provider, and the bundled provider unless another `[providers.*]` entry
still uses it. The resource group is kept; it prints the `az group delete`
command if you want it gone.

### When the subscription owner makes the robot login

If your account may not create a service principal, the owner of the
subscription can make one for you:

```bash
az ad sp create-for-rbac --name agent-deck-sandboxes-sp --role Contributor \
  --scopes /subscriptions/<subscription id>/resourceGroups/agent-deck-sandboxes
```

They send you its `appId` and `password` privately. You run:

```bash
agent-deck provider connect azure --subscription <subscription id> \
  --resource-group agent-deck-sandboxes --sp-app-id <appId> --sp-secret-stdin
```

and paste the secret when asked. It is not shown and never goes in argv. To
pipe it from a file: `... --sp-secret-stdin < secret.txt`. agent-deck skips
creating a service principal and uses this one.

### Where files go

| File | Path | Mode |
|------|------|------|
| Secret | `~/.config/agent-deck/azure-sp-secret` | 0600 |
| Settings | `~/.config/agent-deck/azure-vm.conf` | 0600 |
| Provider copy | `~/.local/share/agent-deck/providers/azure-vm/` | dir 0700 |
| Provider entry | `[providers.azure]` in `~/.config/agent-deck/config.toml` | |

`XDG_CONFIG_HOME` and `XDG_DATA_HOME` move these as usual. The secret file
holds just the secret (no trailing newline), written atomically, and is
never printed or logged. While a new secret is being stored,
`azure-vm.conf` carries `sp_secret_pending = true`; if a run stops before
the secret is saved, the next run sees the marker and issues a fresh secret
instead of trusting the old file. macOS uses the same
0600 file (the provider reads `sp_secret_file`).

### Troubleshooting

**"I am logged in to portal.azure.com, why does it ask me to log in?"** The
browser login does not log in the command line. The command says so:

```
Being logged in to portal.azure.com in a browser does not log in this command; it needs its own login.
```

Open the link it prints and enter the code.

**No subscription.** A Microsoft account on its own cannot pay for VMs:

```
You are logged in as <you>, but this account has no Azure subscription.
A Microsoft account on its own cannot pay for VMs; it needs a subscription.
To get one:
  1. Open https://portal.azure.com and sign in as <you>.
  2. Search for "Subscriptions" at the top and open it.
  3. Click "+ Add" (on a new account the button says "Start free"), and finish the sign-up.
  4. Run this command again.
```

A free trial that ended must be upgraded: portal.azure.com > Subscriptions >
your subscription > Upgrade.

**Wrong directory (tenant).** Your subscription is in another directory, for
example your company's:

```
The subscription you want is not in the directory you logged in to (<tenant>).
Your account can see subscriptions in: <tenants>
Run again with the right directory:
  agent-deck provider connect azure --tenant <directory id>
```

If login itself fails with `Azure says this account is not in directory ...`,
check the `--tenant` value, or leave it out.

If you are already logged in to `az`, but in another directory than the
subscription's, it stops before changing anything:

```
Subscription "<name>" (<id>) is in directory <tenant>, but your az login is for directory <other tenant>.
Log in to that directory, then run this again:
  az login --tenant <tenant>
```

**No permission to create a service principal.**

```
Your account may not create a service principal (a robot login for agent-deck) here.
```

It then prints three ways out for the subscription owner: run the command
on their own machine, make the robot login for you (see
[above](#when-the-subscription-owner-makes-the-robot-login)), or give you the
"Owner" or "User Access Administrator" role on the group and the
"Application Developer" role in Microsoft Entra ID. Similar texts cover no
permission to create the group (`Your account may not create resource group
...`) or to enable `Microsoft.Compute` (`This subscription has not enabled
... yet`).

**Region quota.**

```
Region <region> has no room for a Standard_B2s VM in this subscription: <quota> allows <n> vCPUs, <n> in use, <n> needed.
Fix one of these:
  - Use another region: agent-deck provider connect azure --location <region>   (with a new --resource-group, a group cannot move)
  - Ask for more: portal.azure.com > Subscriptions > your subscription > Usage + quotas > search "<quota>" > request an increase.
  - Use a smaller size: --vm-size Standard_B1s
Free trial subscriptions allow only a few vCPUs per region.
```

**The region is not accepting new customers.** Azure refuses new resources
in some regions for some subscriptions (often new or free ones) with
`RequestDisallowedByAzure ... The selected region is currently not accepting
new customers`. A resource group can still be created there; the first
real resource fails. connect tests this with a free network security group.
Without `--location` it moves on by itself:

```
westeurope is not accepting new customers for this subscription right now; using swedencentral instead.
```

With `--location` it stops and lists regions that passed the same test:

```
Region westeurope is not accepting new customers for this subscription right now.
Azure refused a test resource there: RequestDisallowedByAzure (https://aka.ms/locationineligible).
The resource group can stay where it is; the VMs can live in another region.
These regions take new resources for this subscription: swedencentral, northeurope, germanywestcentral
Run again with one of them:
  agent-deck provider connect azure --location swedencentral
```

If `agent-deck provider test azure --create` fails this way, the provider
prints `hint: region <region> is not taking new customers; set location =
swedencentral ...`: set `location` in `azure-vm.conf`, or run
`agent-deck provider connect azure` again.

**The VM size is not offered.** `SkuNotAvailable ... Standard_B2s is
currently not available in location ...`. A region can stop offering a size
to a subscription. connect checks with `az vm list-skus` and, when you did
not ask for a size, picks another small one:

```
Standard_B2s is not offered in swedencentral for this subscription; using Standard_B2als_v2 (2 vCPUs, about the same price).
```

When you asked for the size (`--vm-size`, or `vm_size` in `azure-vm.conf`)
it stops and lists the small sizes that are offered:

```
VM size Standard_B2s is not offered in swedencentral for this subscription right now.
Small sizes that are offered there: Standard_B2als_v2, Standard_B2as_v2, Standard_B2s_v2, Standard_D2as_v5, Standard_D2s_v5
Run again with one of them:
  agent-deck provider connect azure --vm-size <size>
```

To look yourself: `az vm list-skus -l <region> --size Standard_B2
--resource-type virtualMachines -o table` (it can take a minute or two; a
size that is missing or shows `NotAvailableForSubscription` cannot be used).
The provider's hint on a failed create says the same: `hint: Standard_B2s
is not offered in <region>; set vm_size = Standard_B2als_v2 or see az vm
list-skus -l <region> --size Standard_B2`.

**`MissingSubscriptionRegistration`.** `The subscription is not registered
to use namespace 'Microsoft.Network'`. A new subscription starts with
`Microsoft.Compute` and `Microsoft.Network` off, and the robot login may not
turn them on. connect turns both on first, as you
(`Enabling Microsoft.Network in this subscription (one time, takes a
minute)...`). If you set things up by hand, the subscription owner runs
`az provider register --namespace Microsoft.Network` (and
`Microsoft.Compute`) once; the provider's hint on a failed create says so.

**The check failed.** `Setup is saved, but the check failed`. Azure can be
slow to see a new service principal. Try again later with
`agent-deck provider test azure`. When the stored robot login was reused,
the message ends with `If the secret expired or was reset, run: agent-deck
provider connect azure --reset-secret`.

**The service principal name is taken.**

```
A service principal named agent-deck-sandboxes-sp already exists (app id <id>), and this machine did not set it up.
```

Another machine (or a person) uses it. Pick another name with `--sp-name`,
or take it over with `--reset-secret` (its old secret stops working).
A run that failed half way (the app was made, its role was not) leaves such
an app behind too.

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
- `destroy` also fails, deletes nothing and keeps the ssh key when the config
  (or `az account set`) no longer points at the resource group and
  subscription the sandbox was made in, so a VM is never left running and
  billed without its key. Its error names both places.
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

## Manual setup

`agent-deck provider connect azure` ([above](#connect-in-one-step)) does all
of this for you. To do it by hand: `az login`, create the resource group,
create a service principal with Contributor on that group only
(`az ad sp create-for-rbac --name <name> --role Contributor --scopes
/subscriptions/<sub>/resourceGroups/<group>`), save its password in a 0600
file, then write the two files below.

`~/.config/agent-deck/config.toml`:

```toml
[providers.azure]
command = "/abs/path/to/agent-deck/examples/providers/azure-vm/provider"
create_timeout_seconds = 900
timeout_seconds = 600        # destroy deletes up to six resources one by one (VM ~1-3 min);
                             # the rollback of a failed create gets this long too
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
# Written by `agent-deck provider connect azure` (the provider ignores them):
# sp_created        = true
# sp_secret_pending = true
# Optional:
# project_git       = https://github.com/you/repo.git
# claude_token_file = /home/you/.config/agent-deck/claude-token
# agent_deck_binary = /home/you/bin/agent-deck-linux-amd64
```

If your region or subscription does not offer `Standard_B2s`, pick another
small size that `az vm list-skus -l <location> --size Standard_B2
--resource-type virtualMachines -o table` lists without restrictions (for
example `Standard_B2als_v2`). If the region is not accepting new customers
(`RequestDisallowedByAzure`), set `location` to another region; the group
can stay where it is.

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
az resource list --tag agent-deck-provider=azure-vm --query "[?resourceGroup=='agent-deck-sandboxes']" -o table
```

(`az resource list` does not take `--tag` together with `--resource-group`,
hence the query.) Deletes take a little while to show in that list: a
resource just destroyed can still be listed for a minute or so.

## Cost

Standard_B2s (2 vCPU, 4 GiB) is a few US cents an hour, plus a little for the
static public IP and the 30 GB disk; check the Azure pricing page for your
region. A sandbox costs money until it is destroyed: destroy it after use.
