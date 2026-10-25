# ssh-vm provider (template)

A starting point for "my VM controller has an HTTP API". It calls the API with
curl and maps the reply to the sandbox object. It does not work as is: fix the
lines marked `TODO` for your API.

## What it assumes

| Action | Call | Reply |
|--------|------|-------|
| create | `POST $VM_API_URL/vms` with `{"name","labels"}` | the VM |
| list | `GET $VM_API_URL/vms?label=agent-deck.provider=ssh-vm` | `{"vms":[...]}` |
| status | `GET $VM_API_URL/vms/<id>` | the VM, 404 when gone |
| destroy | `DELETE $VM_API_URL/vms/<id>` | 2xx, or 404 when gone |

A VM is mapped from `id`, `ip`, `ssh_port`, `ssh_user`, `status`,
`expires_at` and `host_key`.

## Env

- `VM_API_URL` (required): base URL of the API.
- `VM_API_TOKEN` (required): bearer token. It goes into a 0600 curl config
  file in a private temp dir, never into argv.
- `VM_SSH_USER`: login user, default `dev`.
- `VM_SSH_KEY_PATH`: absolute path of a 0600 private key for the VMs. Unset
  means auth `agent`: your ssh-agent must hold the key.

These must be in the environment agent-deck runs in. Missing ones fail with a
clear message.

## Configure

```toml
[providers.vm]
command = "/abs/path/to/my-vm-provider"
create_timeout_seconds = 600
```

## Try it

```bash
export VM_API_URL=https://vms.example.internal/api
read -rs VM_API_TOKEN && export VM_API_TOKEN
echo '{"protocol":1,"action":"list"}' | ./provider list
agent-deck provider test vm --create
```

If your API returns from create before the VM has booted, poll status inside
`create` until the VM has an address. agent-deck waits up to
`create_timeout_seconds`.
