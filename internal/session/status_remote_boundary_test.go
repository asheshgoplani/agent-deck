package session

import (
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestLocalStatusCadencePreservesRemoteRefresh(t *testing.T) {
	for _, seconds := range []int{1, 5, 10} {
		cfg := &UserConfig{Performance: PerformanceSettings{StatusIntervalSeconds: seconds},
			UI: UISettings{RemoteSessionRefreshSecs: 30}}
		if cfg.StatusInterval() != time.Duration(seconds)*time.Second {
			t.Fatal("local cadence was not applied")
		}
		if cfg.UI.GetRemoteSessionRefreshSecs() != 30 {
			t.Fatal("local status cadence altered remote session refresh")
		}
	}
	var cfg UserConfig
	if _, err := toml.Decode("[performance]\nstatus_interval_seconds=5\n[ui]\nremote_session_refresh_secs=30\n", &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.StatusInterval() != 5*time.Second || cfg.UI.GetRemoteSessionRefreshSecs() != 30 {
		t.Fatal("configured local and remote intervals must remain independent")
	}
}
