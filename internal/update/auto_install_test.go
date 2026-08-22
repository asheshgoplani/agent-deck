package update

import (
	"reflect"
	"testing"
)

func TestAutoInstallOrderingFixture(t *testing.T) {
	origCheck, origFetch, origInstall := autoCheckRelease, autoFetchRelease, autoInstallRelease
	var order []string
	autoCheckRelease = func(string) (*UpdateInfo, error) {
		order = append(order, "check")
		return &UpdateInfo{Available: true, LatestVersion: "1.15.0"}, nil
	}
	autoFetchRelease = func(string) (*Release, error) {
		order = append(order, "fetch")
		return &Release{TagName: "v1.15.0"}, nil
	}
	autoInstallRelease = func(*Release) error { order = append(order, "install+rebootstrap"); return nil }
	t.Cleanup(func() { autoCheckRelease, autoFetchRelease, autoInstallRelease = origCheck, origFetch, origInstall })
	if _, err := AutoInstallAvailable("1.14.0"); err != nil {
		t.Fatal(err)
	}
	want := []string{"check", "fetch", "install+rebootstrap"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("ordering = %v, want %v", order, want)
	}
}

func TestReexecLoopGuard(t *testing.T) {
	t.Setenv(UpdatedSentinelEnv, "v1.15.0")
	if !ReexecLoopGuard("1.15.0") {
		t.Fatal("matching sentinel must prevent an update loop")
	}
	if ReexecLoopGuard("1.15.1") {
		t.Fatal("sentinel must not suppress a later release")
	}
}
