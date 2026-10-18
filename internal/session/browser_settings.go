package session

import (
	"fmt"
	"path/filepath"
	"strings"
)

// BrowserSettings is the [browser] section (and [profiles.<name>.browser]).
type BrowserSettings struct {
	// Enabled turns on browser requests for this host: `agent-deck browser
	// <op>` and `events publish --kind macapp.browser.request`. Off by
	// default. Like [macapp] plugins it is an opt-in, not a security
	// boundary: an agent on the host can edit its config. The Mac app
	// enforces the browser policy (docs/macapp-browser.md).
	Enabled *bool `toml:"enabled,omitempty"`
}

// ResolveBrowserEnabled applies [profiles.<profile>.browser].enabled, then
// [browser].enabled, then the default: off.
func ResolveBrowserEnabled(cfg *UserConfig, profile string) bool {
	if cfg == nil {
		return false
	}
	if p, ok := cfg.Profiles[profile]; ok && p.Browser != nil && p.Browser.Enabled != nil {
		return *p.Browser.Enabled
	}
	return cfg.Browser.Enabled != nil && *cfg.Browser.Enabled
}

// BrowserDisabledMessage is the refusal while browser requests are off for
// profile: it names the settings and the commands that turn them on.
func BrowserDisabledMessage(profile string) string {
	if profile == "" {
		profile = DefaultProfile
	}
	return fmt.Sprintf("browser requests are off for profile %q: turn them on with "+
		"`agent-deck config set browser.enabled true` ([browser] enabled = true, every profile) or "+
		"`agent-deck config set profiles.%s.browser.enabled true` ([profiles.%s.browser] enabled = true, this profile only)",
		profile, profile, profile)
}

// profileBrowserKey is the config key profiles.<name>.browser.enabled, the
// per-profile form of browser.enabled; it is resolved by name, not listed in
// the static schema.
func profileBrowserKey(name string) (ConfigKey, bool) {
	rest, ok := strings.CutPrefix(name, "profiles.")
	if !ok {
		return ConfigKey{}, false
	}
	profile, ok := strings.CutSuffix(rest, ".browser.enabled")
	if !ok || profile == "" || strings.Contains(profile, ".") || filepath.Base(profile) != profile || !filepath.IsLocal(profile) {
		return ConfigKey{}, false
	}
	return ConfigKey{Key: name, Section: "Advanced", Label: "Browser requests (" + profile + ")",
		Help: "Override browser.enabled for agent-deck profile " + profile, Type: "bool",
		get: func(c *UserConfig) any {
			if p, ok := c.Profiles[profile]; ok && p.Browser != nil && p.Browser.Enabled != nil {
				return *p.Browser.Enabled
			}
			return ResolveBrowserEnabled(c, profile)
		},
		set: func(c *UserConfig, v any) {
			if c.Profiles == nil {
				c.Profiles = map[string]ProfileSettings{}
			}
			p := c.Profiles[profile]
			p.Browser = &BrowserSettings{Enabled: boolRef(v.(bool))}
			c.Profiles[profile] = p
		}}, true
}
