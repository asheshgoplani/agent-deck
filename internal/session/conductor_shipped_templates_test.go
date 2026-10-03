package session

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shippedConductorTemplate is one row of testdata/conductor_templates_shipped.tsv:
// the sha256 of the instructions file a released agent-deck actually wrote,
// rendered from the template source at that release tag.
type shippedConductorTemplate struct {
	tag, blob, agent, kind, sha256 string
}

func loadShippedConductorTemplates(t *testing.T) []shippedConductorTemplate {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "conductor_templates_shipped.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []shippedConductorTemplate
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			t.Fatalf("malformed fixture row %q", line)
		}
		rows = append(rows, shippedConductorTemplate{fields[0], fields[1], fields[2], fields[3], fields[4]})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no fixture rows")
	}
	return rows
}

// TestShippedConductorTemplatesAreRecognizedAsGenerated proves, against the
// templates that actually shipped (v1.15.0, v1.16.5, v1.16.8, v1.16.10 for
// generation 0; v1.10.9, v1.10.10, v1.10.11 for generation 1; v1.11.0 as the
// generation-0 boundary), that a conductor left on any released instructions
// file is recognised as generated and migrated in place through the full
// conductorInstructionsGenerations list, while a hand-written file is left
// alone.
//
// Fixture rows span exactly 3 distinct blobs: v1.16.24 (the shared template
// before the #2469 human-tier reply format), v1.11.0-v1.16.10 share one, and
// v1.10.9-v1.10.11 share the other (predating #1814's substate-guidance row).
// v1.9.73 and v1.9.70 are intentionally not covered; see the tsv header.
func TestShippedConductorTemplatesAreRecognizedAsGenerated(t *testing.T) {
	rows := loadShippedConductorTemplates(t)

	blobs := map[string]bool{}
	tags := map[string]bool{}
	for _, r := range rows {
		blobs[r.blob] = true
		tags[r.tag] = true
	}
	if len(blobs) != 3 {
		t.Fatalf("shipped templates span %d distinct blobs %v; conductorInstructionsGenerations reconstructs exactly 3 prior generations, update the fixture and the generation count together", len(blobs), blobs)
	}
	for _, tag := range []string{"v1.16.24", "v1.15.0", "v1.16.5", "v1.16.8", "v1.16.10", "v1.11.0", "v1.10.9", "v1.10.10", "v1.10.11"} {
		if !tags[tag] {
			t.Fatalf("fixture is missing shipped tag %s", tag)
		}
	}

	for _, r := range rows {
		t.Run(r.tag+"/"+r.agent+"/"+r.kind, func(t *testing.T) {
			spec, err := GetConductorAgentSpec(r.agent)
			if err != nil {
				t.Fatal(err)
			}
			template, name := conductorSharedClaudeMDTemplate, ""
			if r.kind == "per-name" {
				name = "upgrade-" + r.agent
				template = conductorPerNameClaudeMDTemplate
				if r.agent == ConductorAgentHermes {
					template = conductorPerNameHermesMDTemplate
				}
			}
			current := renderConductorInstructionsTemplate(template, name, DefaultProfile, spec)

			oldGenerations := renderConductorInstructionsGenerations(template, name, DefaultProfile, spec)
			var shipped string
			for _, rendered := range oldGenerations {
				if fmtHash(rendered) == r.sha256 {
					shipped = rendered
				}
			}
			if shipped == "" {
				t.Fatalf("no reconstructed generation matches what %s shipped (%s): a conductor from that release would not be migrated", r.tag, r.sha256)
			}

			dir := t.TempDir()
			generated := filepath.Join(dir, spec.InstructionsFileName)
			if err := os.WriteFile(generated, []byte(shipped), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := writeGeneratedFileOrMigrate(generated, oldGenerations, current, 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(generated)
			if err != nil {
				t.Fatal(err)
			}
			if !matchesTemplateContent(string(got), current) {
				t.Fatalf("instructions file from %s was not recognised as generated and migrated", r.tag)
			}

			handWritten := filepath.Join(dir, "custom-"+spec.InstructionsFileName)
			custom := shipped + "\n## My own rules\n\nNever touch the prod deck.\n"
			if err := os.WriteFile(handWritten, []byte(custom), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := writeGeneratedFileOrMigrate(handWritten, oldGenerations, current, 0o644); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(handWritten); string(got) != custom {
				t.Fatalf("hand-written instructions file was rewritten:\n%s", got)
			}
		})
	}
}

// TestShippedV11624PerNameTemplatesAreCurrent pins that the per-name files
// v1.16.24 wrote (hashes rendered from that tag's source) equal the current
// per-name templates: the #2469 human tier changed only the shared template,
// so these need no migration and carry no fixture generation.
func TestShippedV11624PerNameTemplatesAreCurrent(t *testing.T) {
	for agent, want := range map[string]string{
		ConductorAgentClaude: "fdfb612cd7dd24ebc5ac7ea6512452cba1b653be9d02525dad81dd17ae9372a2",
		ConductorAgentCodex:  "bb1a5c3aa1628595a8b824d14b0a3e335b89c39bd5a90ecc41bb0f64cc0446e8",
		ConductorAgentHermes: "50b0ef27605b216e89d2632656e2d14f8f8a4c1e10f3fed19edc31e1232cba17",
	} {
		spec, err := GetConductorAgentSpec(agent)
		if err != nil {
			t.Fatal(err)
		}
		template := conductorPerNameClaudeMDTemplate
		if agent == ConductorAgentHermes {
			template = conductorPerNameHermesMDTemplate
		}
		if got := fmtHash(renderConductorInstructionsTemplate(template, "upgrade-"+agent, DefaultProfile, spec)); got != want {
			t.Fatalf("%s per-name template changed since v1.16.24 (%s): add a fixture generation for it", agent, got)
		}
	}
}
