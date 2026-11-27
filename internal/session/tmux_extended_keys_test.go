package session

import "testing"

func TestTmuxSettingsExtendedKeysOff(t *testing.T) {
	cases := map[string]bool{
		"off": true, "OFF": true, "Off": true, " off ": true, "off\t": true,
		"on": false, "": false, "always": false, "csi-u": false, "offf": false,
	}
	for v, want := range cases {
		if got := (TmuxSettings{Options: map[string]string{"extended-keys": v}}).ExtendedKeysOff(); got != want {
			t.Errorf("ExtendedKeysOff(%q) = %v, want %v", v, got, want)
		}
	}
	if (TmuxSettings{}).ExtendedKeysOff() {
		t.Error("zero TmuxSettings must not be off")
	}
}
