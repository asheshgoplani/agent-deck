# Fake az for `agent-deck provider connect azure` tests

`az` here is a bash script (bash 3.2 compatible, no python, no network) that
stands in for the Azure CLI. It answers the calls `provider connect azure`
makes (steps 1-10 and disconnect) and the calls the bundled azure-vm
provider makes for `list` (`az login --service-principal ... --password @-`,
`az vm list --show-details --resource-group RG --output json --subscription S`).
Answers come from `recorded/*.json`, shaped like azure-cli 2.6x output;
`__PLACEHOLDERS__` in them are filled in per call. Errors use az's own text
and format (`ERROR: (Code) Message` / `Code:` / `Message:`), exit 1, or 3
for a missing resource group, like az.

Put the directory first in `PATH` (or point `azConnectLookPath` at it).

## Env

| var | meaning |
|---|---|
| `FAKE_AZ_STATE` | state dir, required (made if missing) |
| `FAKE_AZ_SCENARIO` | comma list of switches below |
| `FAKE_AZ_LOGIN_WAIT` | seconds the device-code login waits after printing the code (default 0) |

## Switches (`FAKE_AZ_SCENARIO`)

| switch | effect |
|---|---|
| `logged_out` | `account show` fails `Please run 'az login' to setup account.` until a device-code login succeeds. Without it the user starts logged in. |
| `no_subscription` | `account list --all` has only the tenant-level entry `N/A(tenant level account)`; `account show` returns it. `az login` without `--allow-no-subscriptions` fails `No subscriptions found for dev.tester@outlook.com.` |
| `login_no_subscriptions_error` | device-code login always fails `No subscriptions found for dev.tester@outlook.com.` |
| `multi_sub` | two Enabled subscriptions in the home tenant plus a tenant-level entry for tenant `b2c3d4e5-...-00000000000b` |
| `disabled_sub` | `Free Trial` (Disabled) and `Azure for Students` (Warned) only |
| `other_tenant` | home tenant has only its tenant-level entry; the one subscription (`Contoso Engineering`) is in tenant `b2c3d4e5-...-00000000000b` (`contoso.onmicrosoft.com`) |
| `wrong_tenant_login` | device-code login prints the code, then fails `AADSTS50020: User account 'dev.tester@outlook.com' from identity provider 'live.com' does not exist in tenant '<--tenant>' ...` |
| `no_sp_permission` | `ad sp create-for-rbac` and `ad app delete` fail `Insufficient privileges to complete the operation.`; `role assignment delete` fails AuthorizationFailed for `Microsoft.Authorization/roleAssignments/delete` |
| `no_role_permission` | `ad sp create-for-rbac` makes the app, then fails AuthorizationFailed for `Microsoft.Authorization/roleAssignments/write` |
| `no_group_permission` | `group create` of a new group fails AuthorizationFailed for `Microsoft.Resources/subscriptions/resourcegroups/write` |
| `quota_full` | `vm list-usage`: `cores` 3 of 4 and `standardBSFamily` 3 of 4 used (numbers as JSON numbers; the normal answer has strings) |
| `usage_error` | `vm list-usage` fails (InternalServerError) |
| `unregistered` | `Microsoft.Compute` and `Microsoft.Network` are `NotRegistered` until `provider register`; `vm list-usage`/`vm list` fail `MissingSubscriptionRegistration` while Compute is not registered; `network nsg create/delete` fail with az's real text for Microsoft.Network (smoke test, with `Exception Details`; `recorded/missing-registration.stderr.txt`) |
| `register_denied` | `provider register` fails AuthorizationFailed for `<ns>/register/action` |
| `sp_propagation_delay=N` | the first N service-principal logins fail `AADSTS700016: Application with identifier '<appId>' was not found in the directory ...` |
| `wide_scope` | `role assignment list` also returns a `User Access Administrator` assignment at `/subscriptions/<sub>` |
| `sp_name_taken` | an app named `agent-deck-sandboxes-sp` (appId `f0f0f0f0-0000-4000-8000-000000000900`, password `Taken8Q~secret-used-on-another-machine`, a role on `someone-elses-group`) exists from the first call on, made by someone else |
| `region_closed=<loc>[+<loc>...]` | `network nsg create` in those regions fails with az's real text (smoke test, `recorded/nsg-create-region-closed.stderr.txt`): `ERROR: (RequestDisallowedByAzure) Resource '<name>' was disallowed by Azure: The selected region is currently not accepting new customers: https://aka.ms/locationineligible.` + `Code:`/`Message:`/`Target:` lines, exit 1. `group create` there still works, like Azure. |
| `sku_missing=<size>[+<size>...]` | `vm list-skus` leaves those sizes out entirely (as the real swedencentral listing left out `Standard_B2s`) |
| `sku_restricted=<size>[+<size>...]` | `vm list-skus` gives those sizes a `Location` restriction (`NotAvailableForSubscription`, `values`/`restrictionInfo.locations` = the asked region); without `--all` they are left out, like az |
| `skus_error` | `vm list-skus` fails (InternalServerError) |
| `skus_wait=N` | `vm list-skus` hangs N seconds (the real call took ~80 s), then exits without output |
| `nsg_delete_error` | `network nsg delete` fails (InternalServerError) |
| `login_other_tenant` | the user's `account show` reports tenantId `b2c3d4e5-...-00000000000b` while the subscriptions stay in the home tenant (logged in to another directory); `ad app delete` and `role assignment delete` of an app made in the home tenant answer as if it did not exist (`Resource '<id>' does not exist ...`, `No matched assignments were found to delete`), as az does from another directory |

Other behaviour (no switch):

- `group create` with a location not in the script's `LOCATIONS` list fails
  `LocationNotAvailableForResourceGroup`; an existing group in another
  location fails `InvalidResourceGroupLocation`.
- `account show --subscription S` answers for S, with S's own `tenantId`.
- `--subscription` must be one of the scenario's subscription ids or exact
  names, else `Subscription '<x>' not found. Check the spelling and casing and try again.`
- `ad sp create-for-rbac` with a name it already made patches that app
  (same appId, stderr `Found an existing application instance ...`) and
  issues a new password, like az.
- SP login: secret only via `--password @-` (anything else exits 2). An SP the
  fake made must get its current password (else `AADSTS7000215: Invalid client secret provided`)
  and its tenant; a deleted app gets AADSTS700016. An unknown appId (`--sp-app-id`
  flow) is accepted with any non-empty secret.
- `vm list` as an SP the fake made needs a role on that group (else
  AuthorizationFailed); a missing group gives `ResourceGroupNotFound` (exit 3).
- `ad sp list --display-name X` lists the apps whose name starts with X
  (case-insensitive, like az's Graph `startswith` filter), deleted ones left out.
- `role assignment delete` with nothing matching: `No matched assignments were found to delete`;
  `ad app delete` of an unknown app: `Resource '<id>' does not exist ...` (both exit 1).
- `vm list-skus --location L [--size S] [--resource-type virtualMachines] [--all]`
  answers from `recorded/vm-list-skus.json` (az's shape: a list of
  `{name, resourceType, family, locations, locationInfo, restrictions:[{type,
  reasonCode, values, restrictionInfo}], ...}`; every size there is offered
  unless a switch says otherwise). `--size` is a case-insensitive substring
  of the name, like az (`Standard_B2` also matches `Standard_B20ms`). A
  region not in `LOCATIONS` gives `[]`.
- `network nsg create --name N --resource-group RG --location L` needs
  Microsoft.Network registered and the group (else `ResourceGroupNotFound`,
  exit 3), records `groups/<sub>/<rg>/nsgs/<N>` (its location) and a line
  `<N> <L> <--tags>` in `nsg-created.log`; `network nsg delete` moves it to
  `nsgs-deleted/` and exits 0 when there is nothing to delete, like az.
- An unmodelled command fails `fake az: unexpected command: ...`, exit 2.

Fixed identities (also in `recorded/`): user `dev.tester@outlook.com`, home
tenant `a1b2c3d4-0000-4000-8000-00000000000a`, default subscription
`11111111-1111-4111-8111-111111111111` (`Azure subscription 1`), second
`22222222-2222-4222-8222-222222222222` (`Pay-As-You-Go Dev`).
Device code: `https://microsoft.com/devicelogin`, code `FAKE1234X`.

## State (`$FAKE_AZ_STATE`)

| path | meaning |
|---|---|
| `argv.log` | one line per call: the args joined by `\x1f` |
| `env.log` | one line per call: `AZURE_CONFIG_DIR=<value>` |
| `logged_in` | the user's device-code login happened (`user=`, `tenant=` lines) |
| `default_sub` | set by the user's `az account set` (connect must never call it) |
| `groups/<sub>/<lowercase name>/{name,location,tagged}` | resource groups; tests may pre-make one (no `tagged` file: `"tags": null`) |
| `registered/<namespace>` | providers registered under `unregistered` |
| `sps/<appId>/{name,password,tenant,subscription,scopes,role,object_id,app_object_id}` | SPs made by create-for-rbac; `scopes` one role scope per line |
| `sps-by-name/<display name>` | appId of that name |
| `deleted/<appId>` | apps removed with `ad app delete` |
| `issued_secret` | the last password create-for-rbac issued |
| `sp_login_secret` | the secret the last SP login read from stdin (trailing newline stripped, as az does) |
| `sp_login.count`, `app.count`, `secret.count` | counters |
| `groups/<sub>/<rg>/nsgs/<name>`, `nsgs-deleted/` | network security groups (the region probe); a left-over probe shows up in `nsgs/` |
| `nsg-created.log` | one line per `network nsg create` that succeeded: name, location, tags |
| `vms.json` | optional, tests write it: the `vm list` answer (default `[]`). `recorded/vm-list-one-sandbox.json` is one tagged sandbox; `__SUB_ID__`, `__RG_UPPER__`, `__LOCATION__` are filled in |

An SP login marks `$AZURE_CONFIG_DIR/fake-az-sp-login` (or
`$FAKE_AZ_STATE/default-config/` without `AZURE_CONFIG_DIR`); later calls
with that config dir run as the SP, so the provider's private config dir
works like az's.
