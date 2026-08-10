package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestVerificationPromptTemplatesContract(t *testing.T) {
	repoRoot := filepath.Clean("..")
	promptDir := filepath.Join(repoRoot, "skills", "orchestrate", "references", "prompts")
	renderScript := filepath.Join(promptDir, "render.sh")
	placeholderPattern := regexp.MustCompile(`\{\{([A-Z_]+)\}\}`)
	placeholderNames := func(contents []byte) []string {
		var names []string
		for _, match := range placeholderPattern.FindAllSubmatch(contents, -1) {
			names = append(names, string(match[1]))
		}
		slices.Sort(names)
		return names
	}

	preamble, err := os.ReadFile(filepath.Join(promptDir, "verify-preamble.md"))
	if err != nil {
		t.Fatalf("read verification preamble: %v", err)
	}
	wantPreamblePlaceholders := []string{"FRESHNESS_CUTOFF", "RUN_ID", "SCOPE"}
	if got := placeholderNames(preamble); !slices.Equal(got, wantPreamblePlaceholders) {
		t.Fatalf("preamble placeholders = %v, want %v", got, wantPreamblePlaceholders)
	}
	const writeBoundary = "Write only the artifact path assigned by this prompt and a temporary sibling " +
		"used solely for atomic replacement of that artifact. Remove the temporary sibling if publication " +
		"fails. All other investigation must be read-only."
	if normalized := strings.Join(strings.Fields(string(preamble)), " "); !strings.Contains(normalized, writeBoundary) {
		t.Fatalf("preamble missing complete assigned-artifact write boundary %q", writeBoundary)
	}

	tests := []struct {
		name             string
		placeholders     []string
		renderArgs       []string
		requiredRendered []string
	}{
		{
			name:         "verify-recon",
			placeholders: []string{"ARTIFACT_PATH", "CLAIM", "ENVIRONMENT", "SYSTEM"},
			renderArgs: []string{
				"SYSTEM=production", "CLAIM=service is healthy", "ENVIRONMENT=licensed production",
				"ARTIFACT_PATH=/artifacts/recon.json",
			},
			requiredRendered: []string{
				"deployed version", "source revision", "digest", "licensing state",
				"stable arm ID and the exact question it answers",
				"name its producer, artifact path, expected schema, deciding fields, and freshness requirement",
				"Write one schema-consistent JSON document atomically",
				"completion field set to true in the staged content only after all recon work is complete",
				"Successful publication of that staged document is producer completion",
			},
		},
		{
			name: "verify-arm",
			placeholders: []string{
				"ARM_ID", "ARTIFACT_PATH", "ARTIFACT_SCHEMA", "DECIDING_FIELDS",
				"MEASUREMENT_COMMANDS", "QUESTION",
			},
			renderArgs: []string{
				"ARM_ID=health", "QUESTION=does health pass", "MEASUREMENT_COMMANDS=curl health",
				"ARTIFACT_PATH=/artifacts/health.json", "ARTIFACT_SCHEMA=arm-v1",
				"DECIDING_FIELDS=outcome,status",
			},
			requiredRendered: []string{
				"For every command, record the exact command, exit status, timestamps, and the deciding evidence",
				"The schema-valid artifact must include the arm ID and question, run and system provenance, producer identity",
				"start and completion timestamps, command exits and evidence",
				"Set its explicit completion field to true in the staged content only after measurement is complete",
				"Successful publication of that staged document is producer completion",
				"retain its first command, exit status, and evidence", "Allow at most one clean rerun",
				"repeated product-behavior failure is a defect", "repeated harness, environment, or licensing failure is inconclusive",
			},
		},
		{
			name:         "verify-report",
			placeholders: []string{"ARM_ARTIFACTS", "RECON_ARTIFACT", "REPORT_PATH"},
			renderArgs: []string{
				"RECON_ARTIFACT=/artifacts/recon.json", "ARM_ARTIFACTS=/artifacts/health.json",
				"REPORT_PATH=/artifacts/report.json",
			},
			requiredRendered: []string{
				"fixed recon artifact contract", "Before consuming the arms array",
				"report outcome must be `inconclusive`; it can never be `pass`",
				"Adjudicate conflicting arm evidence explicitly instead of selecting the convenient result",
				"Record each first failure, its diagnosis, whether the single allowed clean rerun occurred, both attempts' evidence, and the repeated-failure classification",
				"Write the consolidated report atomically",
				"Include deployed version, revision and digest; environment and licensing state; authorized scope",
				"each arm ID, question, evidence, and artifact-validation result; contradictions and their adjudication; reruns; provenance; timestamps",
				"completion field to true in the staged content only after report construction is complete",
				"immediately before the final atomic rename",
				"Successful publication of that staged document is producer completion",
				"Record exactly one outcome: `pass`, `defect`, or `inconclusive`",
			},
		},
	}

	commonArgs := []string{
		"RUN_ID=contract-test", "SCOPE=deployed verification only",
		"FRESHNESS_CUTOFF=2026-08-10T18:00:00Z",
	}
	commonSafety := []string{
		"Do not brainstorm, redesign, modify the product",
		"temporary sibling used solely for atomic replacement",
		"validate the expected schema, provenance, producer completion, and freshness",
		"Permit at most one clean rerun by default",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			templatePath := filepath.Join(promptDir, tt.name+".md")
			template, err := os.ReadFile(templatePath)
			if err != nil {
				t.Fatalf("read template: %v", err)
			}
			if !strings.HasPrefix(string(template), "{{include:verify-preamble.md}}\n") {
				t.Fatal("operational template must start with the verification preamble include")
			}

			gotPlaceholders := placeholderNames(template)
			if !slices.Equal(gotPlaceholders, tt.placeholders) {
				t.Fatalf("source placeholders = %v, want %v", gotPlaceholders, tt.placeholders)
			}

			outputPath := filepath.Join(t.TempDir(), tt.name+".md")
			args := []string{renderScript, tt.name, outputPath}
			args = append(args, commonArgs...)
			args = append(args, tt.renderArgs...)
			if output, err := exec.Command("bash", args...).CombinedOutput(); err != nil {
				t.Fatalf("render template: %v\n%s", err, output)
			}
			rendered, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("read rendered prompt: %v", err)
			}
			if strings.Contains(string(rendered), "{{include:") || placeholderPattern.Match(rendered) {
				t.Fatalf("rendered prompt contains an unresolved include or placeholder:\n%s", rendered)
			}
			normalizedRendered := strings.Join(strings.Fields(string(rendered)), " ")
			for _, required := range append(commonSafety, tt.requiredRendered...) {
				if !strings.Contains(normalizedRendered, required) {
					t.Errorf("rendered prompt missing required contract text %q", required)
				}
			}
		})
	}
}
