package ask

import (
	"testing"
	"time"
)

// SetVaultForTest points the vault at path and skips the root-ownership
// check, for tests outside this package (cmd/agent-deck, internal/ui) that
// need a fake vault. It panics outside a test binary (testing.Testing), so a
// release binary can never be redirected: no config key or env var reaches
// the vault variables. Call the returned func to restore them.
func SetVaultForTest(path string) (restore func()) {
	if !testing.Testing() {
		panic("ask.SetVaultForTest called outside a test binary")
	}
	oldPath, oldTrusted, oldPoll := vaultPath, vaultTrusted, directPollEvery
	vaultPath = path
	vaultTrusted = func(string) error { return nil }
	directPollEvery = 200 * time.Millisecond
	return func() { vaultPath, vaultTrusted, directPollEvery = oldPath, oldTrusted, oldPoll }
}
