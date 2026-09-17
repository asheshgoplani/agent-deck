package session

import (
	"reflect"
	"testing"
)

// TestNormalizeUIPreviewFields pins [ui.remote_preview].fields and
// [ui.header].fields validation: known names are lowercased/trimmed and
// kept in the order given (order is render order, so it must not be
// resorted like hidden_tools is), unknown names are reported once (via
// registryLog.Warn, the same startup-warning mechanism normalizeUIHiddenTools
// uses) and dropped rather than silently ignored or kept verbatim.
func TestNormalizeUIPreviewFields(t *testing.T) {
	ui := UISettings{
		RemotePreview: RemotePreviewSettings{Fields: []string{" Harnesses ", "version", "bogus_field", "load"}},
		Header:        HeaderSettings{Fields: []string{"sessions_by_status", "made_up"}},
	}
	normalizeUIPreviewFields(&ui)

	wantRemote := []string{"harnesses", "version", "load"}
	if !reflect.DeepEqual(ui.RemotePreview.Fields, wantRemote) {
		t.Fatalf("RemotePreview.Fields = %v, want %v", ui.RemotePreview.Fields, wantRemote)
	}
	wantHeader := []string{"sessions_by_status"}
	if !reflect.DeepEqual(ui.Header.Fields, wantHeader) {
		t.Fatalf("Header.Fields = %v, want %v", ui.Header.Fields, wantHeader)
	}
}

// TestNormalizeUIPreviewFields_AllUnknownLeavesEmpty pins that a field list
// made entirely of unknown names normalizes to empty, so GetRemotePreviewFields
// / GetHeaderFields fall back to the documented default rather than rendering
// an empty panel/header.
func TestNormalizeUIPreviewFields_AllUnknownLeavesEmpty(t *testing.T) {
	ui := UISettings{RemotePreview: RemotePreviewSettings{Fields: []string{"nope", "also_nope"}}}
	normalizeUIPreviewFields(&ui)
	if len(ui.RemotePreview.Fields) != 0 {
		t.Fatalf("RemotePreview.Fields = %v, want empty", ui.RemotePreview.Fields)
	}
	if got := ui.GetRemotePreviewFields(); !reflect.DeepEqual(got, DefaultRemotePreviewFields) {
		t.Fatalf("GetRemotePreviewFields() = %v, want default %v", got, DefaultRemotePreviewFields)
	}
}

// TestGetRemotePreviewFields_DefaultsWhenUnset pins the exact default order
// specified by the addendum spec: version, sessions_by_status, harnesses,
// load, memory, disk, last_poll.
func TestGetRemotePreviewFields_DefaultsWhenUnset(t *testing.T) {
	var ui UISettings
	want := []string{"version", "sessions_by_status", "harnesses", "load", "memory", "disk", "last_poll"}
	if got := ui.GetRemotePreviewFields(); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetRemotePreviewFields() = %v, want %v", got, want)
	}
}

// TestGetHeaderFields_DefaultsWhenUnset pins the header's default field
// order — the subset of the shared vocabulary the header already rendered
// before this config block existed (no harnesses/last_poll segment).
func TestGetHeaderFields_DefaultsWhenUnset(t *testing.T) {
	var ui UISettings
	want := []string{"version", "sessions_by_status", "load", "memory", "disk"}
	if got := ui.GetHeaderFields(); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetHeaderFields() = %v, want %v", got, want)
	}
}

// TestGetRemotePreviewFields_ExplicitOrderPreserved pins that a configured
// list is returned verbatim (order is render order, never resorted).
func TestGetRemotePreviewFields_ExplicitOrderPreserved(t *testing.T) {
	ui := UISettings{RemotePreview: RemotePreviewSettings{Fields: []string{"harnesses", "memory", "version"}}}
	want := []string{"harnesses", "memory", "version"}
	if got := ui.GetRemotePreviewFields(); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetRemotePreviewFields() = %v, want %v", got, want)
	}
}

// TestLoadUserConfig_NormalizesPreviewFieldsOnLoad exercises the config-check
// path end to end: a config.toml with a typo'd field name loads without
// erroring, and the bogus entry is dropped by the time UI.GetRemotePreviewFields
// is consulted (LoadUserConfig is the single funnel every caller — TUI, web,
// CLI — goes through, so this is "reported once at startup", not per-render).
func TestLoadUserConfig_NormalizesPreviewFieldsOnLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	cfg := &UserConfig{UI: UISettings{
		RemotePreview: RemotePreviewSettings{Fields: []string{"version", "typo_field", "harnesses"}},
	}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	loaded, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	want := []string{"version", "harnesses"}
	if got := loaded.UI.GetRemotePreviewFields(); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetRemotePreviewFields() after load = %v, want %v", got, want)
	}
}
