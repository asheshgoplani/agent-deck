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
// templates that actually shipped (v1.15.0, v1.16.5, v1.16.8, v1.16.10),
// that a conductor left on any released instructions file is recognised as
// generated and migrated in place, while a hand-written file is left alone.
//
// Every shipped tag carries the same template blob, so "two or more releases
// behind" is the same single prior generation that
// previousConductorInstructionsTemplate reconstructs; there is no older one
// to recognise.
func TestShippedConductorTemplatesAreRecognizedAsGenerated(t *testing.T) {
	rows := loadShippedConductorTemplates(t)

	blobs := map[string]bool{}
	tags := map[string]bool{}
	for _, r := range rows {
		blobs[r.blob] = true
		tags[r.tag] = true
	}
	if len(blobs) != 1 {
		t.Fatalf("shipped templates span %d distinct blobs %v; previousConductorInstructionsTemplate reconstructs exactly one generation, add another for each extra blob", len(blobs), blobs)
	}
	for _, tag := range []string{"v1.15.0", "v1.16.5", "v1.16.8", "v1.16.10"} {
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
			shipped := renderConductorInstructionsTemplate(previousConductorInstructionsTemplate(template), name, DefaultProfile, spec)
			if got := fmtHash(shipped); got != r.sha256 {
				t.Fatalf("reconstructed prior template hash %s != what %s shipped (%s): a conductor from that release would not be migrated", got, r.tag, r.sha256)
			}
			current := renderConductorInstructionsTemplate(template, name, DefaultProfile, spec)

			dir := t.TempDir()
			generated := filepath.Join(dir, spec.InstructionsFileName)
			if err := os.WriteFile(generated, []byte(shipped), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := writeGeneratedFileOrMigrate(generated, shipped, current, 0o644); err != nil {
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
			if err := writeGeneratedFileOrMigrate(handWritten, shipped, current, 0o644); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(handWritten); string(got) != custom {
				t.Fatalf("hand-written instructions file was rewritten:\n%s", got)
			}
		})
	}
}
