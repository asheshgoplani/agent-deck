package harness

import (
	"reflect"
	"testing"
)

func TestKimiMetadata(t *testing.T) {
	var found []Entry
	for _, entry := range All(nil) {
		if entry.Name == "kimi" {
			found = append(found, entry)
		}
	}
	if len(found) != 1 {
		t.Fatalf("Kimi entries = %d, want exactly one", len(found))
	}
	entry := found[0]
	if entry.DisplayName != "Kimi Code" || entry.Binary != "kimi" {
		t.Errorf("identity = %+v", entry)
	}
	if !reflect.DeepEqual(entry.VersionArgs, []string{"--version"}) {
		t.Errorf("VersionArgs = %v", entry.VersionArgs)
	}
	if entry.InstallCommand != "curl -fsSL https://code.kimi.com/kimi-code/install.sh | bash" {
		t.Errorf("InstallCommand = %q", entry.InstallCommand)
	}
	if entry.LoginCommand != "kimi login" {
		t.Errorf("LoginCommand = %q", entry.LoginCommand)
	}
	if entry.StatusArgs != nil {
		t.Errorf("StatusArgs = %v, want no sign-in status claim", entry.StatusArgs)
	}
	if entry.DocsURL != "https://www.kimi.com/code/docs/en/kimi-code-cli/guides/getting-started.html" {
		t.Errorf("DocsURL = %q", entry.DocsURL)
	}
	if entry.Images {
		t.Error("Images = true, want unverified image capability disabled")
	}
}
