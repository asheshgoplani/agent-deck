package assistant

import "testing"

// connect --json carries the ready skill file itself, so an assistant can
// write it to skill_path without fetching skill_url (which only exists once
// the docs are on main, and needs network access).
func TestConnectionCarriesSkillContent(t *testing.T) {
	for _, id := range SkillKinds() {
		c := goldenConnection(t, id)
		if c.SkillContent != SkillFile(id) {
			t.Errorf("%s: skill_content differs from SkillFile", id)
		}
	}
	if c := goldenConnection(t, "http"); c.SkillContent != "" {
		t.Errorf("http: unexpected skill_content")
	}
}
