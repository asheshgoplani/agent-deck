// Package azurevm embeds the azure-vm example provider so that
// `agent-deck provider connect azure` can install it without a source
// checkout.
package azurevm

import "embed"

// Files holds the provider script, the sandbox setup scripts and the
// built-in demo project, at their paths in this directory.
//
//go:embed provider sandbox-init.sh sandbox-setup.sh demo-project
var Files embed.FS
