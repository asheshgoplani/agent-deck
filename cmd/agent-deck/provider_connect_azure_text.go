package main

import (
	"fmt"
	"strings"
)

// Every user-facing text of `agent-deck provider connect azure`. Tests and
// docs quote these, so change them here only.

// Error kinds (the "kind" of a JSON error event).
const (
	azKindAzMissing             = "az_missing"
	azKindLoginFailed           = "login_failed"
	azKindWrongTenant           = "wrong_tenant"
	azKindNoSubscription        = "no_subscription"
	azKindSubscriptionDisabled  = "subscription_disabled"
	azKindSubscriptionNotFound  = "subscription_not_found"
	azKindChooseSubscription    = "choose_subscription"
	azKindNoPermissionRegister  = "no_permission_register"
	azKindNoPermissionGroup     = "no_permission_group"
	azKindBadLocation           = "bad_location"
	azKindQuota                 = "quota"
	azKindNoPermissionSP        = "no_permission_sp"
	azKindCheckFailed           = "check_failed"
	azKindSandboxesExist        = "sandboxes_exist"
	azKindAzFailed              = "az_failed"
	azKindUsage                 = "usage"
	azKindStoreFailed           = "store_failed"
	azKindAborted               = "aborted"
	azKindListFailed            = "list_failed"
	azKindUnknownConnectTarget  = "unknown_provider"
	azKindSecretInvalid         = "secret_invalid"
	azKindDisconnectNotRequired = "not_connected"
	azKindSPNameTaken           = "sp_name_taken"
	azKindBadConfig             = "bad_config"
	azKindUnsupportedOS         = "unsupported_os"
	azKindRegionClosed          = "region_closed"
	azKindSizeUnavailable       = "size_unavailable"
)

const azTextAzMissingDarwin = `The Azure CLI (az) is not installed. Install it, then run this again:
  brew update && brew install azure-cli`

const azTextAzMissingLinux = `The Azure CLI (az) is not installed. Install it, then run this again:
  curl -sL https://aka.ms/InstallAzureCLIDeb | sudo bash      (Debian, Ubuntu)
  Other Linux: https://learn.microsoft.com/cli/azure/install-azure-cli-linux`

const azTextAzMissingWindows = `The Azure CLI (az) is not installed. Install it, then run this again:
  winget install -e --id Microsoft.AzureCLI`

// azTextAzMissing is the install hint for goos.
func azTextAzMissing(goos string) string {
	switch goos {
	case "darwin":
		return azTextAzMissingDarwin
	case "windows":
		return azTextAzMissingWindows
	default:
		return azTextAzMissingLinux
	}
}

const azTextLoginIntro = `Logging in to Azure with a one-time code.
Being logged in to portal.azure.com in a browser does not log in this command; it needs its own login.
This works over ssh: you can open the link on any phone or computer.`

const azTextDeviceCodeFmt = `
  1. Open:        %s
  2. Enter code:  %s
  3. Sign in with the Microsoft account that owns (or can use) your Azure subscription.

Waiting for you to finish (up to 15 minutes)...`

func azTextDeviceCode(url, code string) string { return fmt.Sprintf(azTextDeviceCodeFmt, url, code) }

const azTextUsingLoginFmt = "Using your az login: %s"

const azTextNoSubscriptionFmt = `You are logged in as %s, but this account has no Azure subscription.
A Microsoft account on its own cannot pay for VMs; it needs a subscription.
To get one:
  1. Open https://portal.azure.com and sign in as %s.
  2. Search for "Subscriptions" at the top and open it.
  3. Click "+ Add" (on a new account the button says "Start free"), and finish the sign-up.
  4. Run this command again.
If your subscription belongs to another directory (for example your company's), run:
  agent-deck provider connect azure --tenant <directory id or domain>`

func azTextNoSubscription(user string) string {
	return fmt.Sprintf(azTextNoSubscriptionFmt, user, user)
}

const azTextWrongTenantFmt = `The subscription you want is not in the directory you logged in to (%s).
Your account can see subscriptions in: %s
Run again with the right directory:
  agent-deck provider connect azure --tenant <directory id>
You can see a subscription's directory in the portal: Subscriptions > your subscription > Overview > "Parent management group" / "Directory".`

func azTextWrongTenant(tenant, others string) string {
	return fmt.Sprintf(azTextWrongTenantFmt, tenant, others)
}

const azTextWrongTenantLoginFmt = `Azure says this account is not in directory %s (%s). Check the --tenant value, or leave it out to use your account's own directory.`

func azTextWrongTenantLogin(tenant, code string) string {
	return fmt.Sprintf(azTextWrongTenantLoginFmt, tenant, code)
}

const azTextLoginFailedFmt = `The Azure login did not finish.
Azure said: %s
Run this command again to get a new code.`

func azTextLoginFailed(said string) string { return fmt.Sprintf(azTextLoginFailedFmt, said) }

const azTextLoginTimedOut = "nobody entered the code within 15 minutes"

const azTextSubscriptionDisabledFmt = `None of your subscriptions can be used right now:
%s
A free trial that ended must be upgraded: portal.azure.com > Subscriptions > your subscription > Upgrade.`

func azTextSubscriptionDisabled(list string) string {
	return fmt.Sprintf(azTextSubscriptionDisabledFmt, list)
}

const azTextSubscriptionNotFoundFmt = "No subscription matches %q. Usable subscriptions:\n%s"

func azTextSubscriptionNotFound(want, list string) string {
	return fmt.Sprintf(azTextSubscriptionNotFoundFmt, want, list)
}

const azTextChooseSubscriptionFmt = "You have several subscriptions. Pick one with --subscription <id>:\n%s"

func azTextChooseSubscription(list string) string {
	return fmt.Sprintf(azTextChooseSubscriptionFmt, list)
}

const (
	azTextSubscriptionMenuIntro = "You have several subscriptions:"
	azTextSubscriptionMenuAsk   = "Pick one [1-%d]: "
	azTextSubscriptionMenuBad   = "Not a number from the list. Nothing was changed."
	azTextSubscriptionFmt       = "Subscription: %s (%s)"
)

const azTextNoPermissionSPFmt = `Your account may not create a service principal (a robot login for agent-deck) here.
Azure said: %s
Ask the owner of subscription "%s" to do one of these:
  a) Run this on their machine, signed in as themselves:
       agent-deck provider connect azure --subscription %s --resource-group %s
     and send you nothing; it stores the login on their machine only.
  b) Make the robot login for you and give you its app id and secret privately:
       az ad sp create-for-rbac --name %s --role Contributor --scopes /subscriptions/%s/resourceGroups/%s
     Then you run:
       agent-deck provider connect azure --subscription %s --resource-group %s --sp-app-id <appId> --sp-secret-stdin
     and paste the secret when asked (it is not shown).
  c) Give you the "Owner" or "User Access Administrator" role on resource group %s, and the
     "Application Developer" role in Microsoft Entra ID, then run this again.`

func azTextNoPermissionSP(said, subName, subID, rg, spName string) string {
	return fmt.Sprintf(azTextNoPermissionSPFmt, said, subName, subID, rg, spName, subID, rg, subID, rg, rg)
}

// azTextNoPermissionSPDeleteFmt is the disconnect variant of NO_PERMISSION_SP.
const azTextNoPermissionSPDeleteFmt = `Your account may not remove service principal %s (the robot login for agent-deck).
Azure said: %s
Ask the owner of subscription %s to run:
  az role assignment delete --assignee %s --scope /subscriptions/%s/resourceGroups/%s --subscription %s
  az ad app delete --id %s
The local Azure settings were kept; run this again once it is removed.`

func azTextNoPermissionSPDelete(appID, said, subID, rg string) string {
	return fmt.Sprintf(azTextNoPermissionSPDeleteFmt, appID, said, subID, appID, subID, rg, subID, appID)
}

const azTextNoPermissionGroupFmt = `Your account may not create resource group %s in subscription "%s".
Azure said: %s
Ask the subscription owner to create it (az group create --name %s --location %s --subscription %s)
and give you "Owner" on it, then run this again.`

func azTextNoPermissionGroup(rg, subName, said, loc, subID string) string {
	return fmt.Sprintf(azTextNoPermissionGroupFmt, rg, subName, said, rg, loc, subID)
}

const azTextNoPermissionRegisterFmt = `This subscription has not enabled %s yet, and your account may not enable it.
Ask the subscription owner to run: az provider register --namespace %s --subscription %s`

func azTextNoPermissionRegister(ns, subID string) string {
	return fmt.Sprintf(azTextNoPermissionRegisterFmt, ns, ns, subID)
}

const azTextBadLocationFmt = `Azure does not offer resource groups in %q for this subscription. Pick another, for example: --location westeurope (see: az account list-locations -o table).`

func azTextBadLocation(loc string) string { return fmt.Sprintf(azTextBadLocationFmt, loc) }

const azTextQuotaFmt = `Region %s has no room for a %s VM in this subscription: %s allows %d vCPUs, %d in use, %d needed.
Fix one of these:
  - Use another region: agent-deck provider connect azure --location <region>   (with a new --resource-group, a group cannot move)
  - Ask for more: portal.azure.com > Subscriptions > your subscription > Usage + quotas > search "%s" > request an increase.
  - Use a smaller size: --vm-size Standard_B1s
Free trial subscriptions allow only a few vCPUs per region.`

func azTextQuota(loc, size, quotaName string, limit, used, need int) string {
	return fmt.Sprintf(azTextQuotaFmt, loc, size, quotaName, limit, used, need, quotaName)
}

const azTextScopeWiderFmt = `Warning: service principal %s has roles outside resource group %s:
%s
agent-deck only needs Contributor on that group. Remove the others in the portal (Access control (IAM)).`

func azTextScopeWider(appID, rg, list string) string {
	return fmt.Sprintf(azTextScopeWiderFmt, appID, rg, list)
}

const azTextCheckFailedFmt = `Setup is saved, but the check failed: agent-deck could not list VMs in %s as the service principal.
%s
Run the check again later with: agent-deck provider test azure`

func azTextCheckFailed(rg, said string) string { return fmt.Sprintf(azTextCheckFailedFmt, rg, said) }

// azTextCheckFailedResetHint is added to CHECK_FAILED when the check used a
// kept (stored) service principal.
const azTextCheckFailedResetHint = "If the secret expired or was reset, run: agent-deck provider connect azure --reset-secret"

const azTextSPNameTakenFmt = `A service principal named %s already exists (app id %s), and this machine did not set it up.
Reusing it would give it a new secret and revoke the secret that other machines or people use with it.
Pick one:
  - Use another name: agent-deck provider connect azure --sp-name <other name>
  - Take it over on purpose (its old secret stops working): agent-deck provider connect azure --reset-secret`

func azTextSPNameTaken(name, appID string) string {
	return fmt.Sprintf(azTextSPNameTakenFmt, name, appID)
}

const azTextWrongTenantAccountFmt = `Subscription "%s" (%s) is in directory %s, but your az login is for directory %s.
Log in to that directory, then run this again:
  az login --tenant %s`

func azTextWrongTenantAccount(subName, subID, subTenant, loginTenant string) string {
	return fmt.Sprintf(azTextWrongTenantAccountFmt, subName, subID, subTenant, loginTenant, subTenant)
}

const azTextBadConfigFmt = "%s has an invalid %s value %q. Fix or remove that line, then run this again."

func azTextBadConfig(confPath, key, value string) string {
	return fmt.Sprintf(azTextBadConfigFmt, confPath, key, value)
}

const azTextWindowsUseWSL = `The bundled Azure provider is a bash script and does not run on Windows.
Run agent-deck inside WSL (Windows Subsystem for Linux) and connect there,
or pass --provider-command <path> to a provider that runs on Windows.`

const azTextSandboxesExistFmt = `%d sandbox(es) still exist in %s: %s
Destroy them first (agent-deck remote destroy <name>), or add --force to disconnect anyway (they keep running and cost money).`

func azTextSandboxesExist(n int, rg, ids string) string {
	return fmt.Sprintf(azTextSandboxesExistFmt, n, rg, ids)
}

const azTextConnectedFmt = `%s
  Subscription:    %s (%s)
  Resource group:  %s (%s)
  Robot login:     %s (Contributor on that group only)
  Secret:          %s (0600, never shown)
  Settings:        %s
%s
Next: agent-deck launch --provider azure -c claude <project path>`

const azTextCheckPassedFmt = "Check passed: the robot login sees resource group %s (%d sandbox(es) now)." //nolint:gosec // G101: user-facing text, not a credential

func azTextCheckPassed(rg string, sandboxes int) string {
	return fmt.Sprintf(azTextCheckPassedFmt, rg, sandboxes)
}

const (
	azTextConnectedFirst        = "Azure is connected."
	azTextAlreadyConnectedFirst = "Azure is already connected (nothing changed)."
)

func azTextConnected(unchanged bool, subName, subID, rg, loc, appID, secretPath, confPath string, sandboxes int) string {
	first := azTextConnectedFirst
	if unchanged {
		first = azTextAlreadyConnectedFirst
	}
	return fmt.Sprintf(azTextConnectedFmt, first, subName, subID, rg, loc, appID, secretPath, confPath, azTextCheckPassed(rg, sandboxes))
}

// REGION_CLOSED: the --location region refuses new resources for this
// subscription (RequestDisallowedByAzure).
const azTextRegionClosedFmt = `Region %s is not accepting new customers for this subscription right now.
Azure refused a test resource there: RequestDisallowedByAzure (https://aka.ms/locationineligible).
The resource group can stay where it is; the VMs can live in another region.
%s`

// The last part of REGION_CLOSED: regions that passed the same test, or none.
const (
	azTextRegionClosedAltsFmt = `These regions take new resources for this subscription: %s
Run again with one of them:
  agent-deck provider connect azure --location %s`
	azTextRegionClosedNoAltsFmt = `None of these regions took a test resource either: %s
Pick another region (see: az account list-locations -o table) and run again:
  agent-deck provider connect azure --location <region>`
)

func azTextRegionClosed(loc string, working, tried []string) string {
	tail := fmt.Sprintf(azTextRegionClosedNoAltsFmt, azJoinOrNone(tried))
	if len(working) > 0 {
		tail = fmt.Sprintf(azTextRegionClosedAltsFmt, strings.Join(working, ", "), working[0])
	}
	return fmt.Sprintf(azTextRegionClosedFmt, loc, tail)
}

// SIZE_UNAVAILABLE: the VM size asked for (--vm-size or vm_size in
// azure-vm.conf) is not offered in the region for this subscription.
const azTextSizeUnavailableFmt = `VM size %s is not offered in %s for this subscription right now.
Small sizes that are offered there: %s
Run again with one of them:
  agent-deck provider connect azure --vm-size <size>
(--vm-size also updates vm_size in %s.)`

func azTextSizeUnavailable(size, loc string, offered []string, confPath string) string {
	return fmt.Sprintf(azTextSizeUnavailableFmt, size, loc, azJoinOrNone(offered), confPath)
}

func azJoinOrNone(list []string) string {
	if len(list) == 0 {
		return "(none)"
	}
	return strings.Join(list, ", ")
}

// Region and size notes (step "region").
const (
	// The default size is not offered; connect picked the next small one.
	azTextSizeUsingFmt = "%s is not offered in %s for this subscription; using %s (%d vCPUs, about the same price)."
	// The default region refuses new resources; connect picked another.
	azTextRegionUsingFmt = "%s is not accepting new customers for this subscription right now; using %s instead."
	// The default region offers none of the small sizes; connect picked another.
	azTextRegionNoSizeUsingFmt = "None of the small VM sizes agent-deck uses is offered in %s for this subscription; using %s instead."
	azTextRegionOKFmt          = "Region %s takes new resources and offers %s."
	azTextRegionKeptFmt        = "Region %s and size %s are already set up; not testing them again."
	azTextRegionProbeFailedFmt = "Could not test whether region %s takes new resources (%s); continuing."
	azTextProbeDeleteFailedFmt = "Could not delete the test network security group %s in %s; delete it with: az network nsg delete --name %s --resource-group %s --subscription %s"
	azTextSkusUnreadableFmt    = "Could not read the VM sizes offered in %s (%s); continuing with %s."
	azTextSkusTimedOutFmt      = "no answer within %s"
)

// Progress and notes.
const (
	azTextRegisteringFmt       = "Enabling %s in this subscription (one time, takes a minute)..."
	azTextRegisteredFmt        = "%s is enabled."
	azTextGroupExistsFmt       = "Resource group %s exists in %s."
	azTextGroupLocationNoteFmt = "Note: --location %s is ignored; the existing group stays in %s."
	azTextGroupCreatedFmt      = "Created resource group %s in %s."
	azTextQuotaUnreadableFmt   = "Could not read the vCPU quota for %s; continuing."
	azTextQuotaOKFmt           = "vCPU quota in %s: %d free (a %s VM needs %d)."
	azTextSPKeptFmt            = "Service principal %s is already set up; keeping its secret."
	azTextSPCreatingFmt        = "Creating service principal %s (Contributor on resource group %s only)..."
	azTextSPCreatedFmt         = "Service principal %s is ready."
	azTextSPGivenFmt           = "Using service principal %s (secret from stdin)."
	azTextRoleCleanFmt         = "Role: Contributor on resource group %s only."
	azTextRoleUnreadable       = "Could not read the service principal's roles; skipping the scope check."
	azTextKeepingCommandFmt    = "Keeping your [providers.%s] command: %s"
	azTextStoredFmt            = "Saved the settings in %s."
	azTextWaitingForSP         = "Waiting for Azure to see the new service principal..."
	azTextSecretPrompt         = "Paste the service principal secret (it is not shown): " //nolint:gosec // G101: user-facing prompt, not a credential
	azTextSecretEmpty          = "The secret on stdin is empty."
	azTextSecretMultiLine      = "The secret on stdin must be one line."
	azTextSecretTooLong        = "The secret on stdin is too long."
)

// Disconnect texts.
const (
	azTextNotConnected           = "Azure is not connected (nothing to remove)."
	azTextDisconnectConfirm      = "Remove service principal %s from Azure and delete the local Azure settings? [y/N] "
	azTextDisconnectConfirmLocal = "Delete the local Azure settings? Service principal %s stays in Azure. [y/N] "
	azTextSPNotOursFmt           = "Service principal %s was not created by this command (it came from --sp-app-id), so it stays in Azure; its owner can remove it."
	azTextDisconnectNeedsYes     = "Refusing to disconnect without --yes (not a terminal)."
	azTextDisconnectAborted      = "Aborted. Nothing was changed."
	azTextGroupKeptFmt           = "The resource group %s is kept. To delete it and everything in it: az group delete --name %s --subscription %s"
	azTextDisconnected           = "Disconnected."
	azTextSPRemovedFmt           = "Removed service principal %s from Azure."
	azTextLocalRemoved           = "Removed the local Azure settings."
	azTextListFailedFmt          = "Could not list the sandboxes of provider %s: %s\nAdd --force to disconnect anyway."
	azTextListFailedForcedFmt    = "Warning: could not list the sandboxes of provider %s (%s); disconnecting anyway (--force)."
)

const azTextDisconnectWrongTenantFmt = `Service principal %s is in directory %s, but your az login is for directory %s.
Nothing was removed. Log in to that directory, then run this again:
  az login --tenant %s`

func azTextDisconnectWrongTenant(appID, spTenant, loginTenant string) string {
	return fmt.Sprintf(azTextDisconnectWrongTenantFmt, appID, spTenant, firstNonEmpty(loginTenant, "(unknown)"), spTenant)
}

const azTextDisconnectWrongSubscriptionFmt = `Your az login does not reach subscription %s in directory %s, where service principal %s was made.
Azure said: %s
Nothing was removed. Log in to that directory, then run this again:
  az login --tenant %s`

func azTextDisconnectWrongSubscription(subID, spTenant, appID, said string) string {
	return fmt.Sprintf(azTextDisconnectWrongSubscriptionFmt, subID, spTenant, appID, said, spTenant)
}

const azTextDisconnectTenantUnknownFmt = `%s has no sp_tenant and no subscription, so disconnect cannot tell which directory service principal %s is in.
Nothing was removed. Add sp_tenant = <directory id> to that file, then run this again.`

func azTextDisconnectTenantUnknown(confPath, appID string) string {
	return fmt.Sprintf(azTextDisconnectTenantUnknownFmt, confPath, appID)
}

const azTextSubInOtherTenantFmt = "the subscription is in directory %s"

const azTextSecretElsewhereKeptFmt = "Left %s in place: sp_secret_file points there, but it does not look like the secret file connect writes. Remove it yourself if nothing else uses it." //nolint:gosec // G101: user-facing text, not a credential

// Usage and generic errors.
const (
	azTextUnknownTargetFmt = "unknown provider to connect %q (known: azure)"
	azTextAzFailedFmt      = "az %s failed: %s"
	azTextAzUnexpectedFmt  = "Error: az %s returned an unexpected %s %q"
	azTextConfFileHeader   = "# Written by agent-deck provider connect azure. Keys below the marker are yours."
	azTextConfFileMarker   = "# ---- marker: add your own keys (project_git, claude_token_file, ...) below ----"
)
