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

			var gotPlaceholders []string
			for _, match := range placeholderPattern.FindAllSubmatch(template, -1) {
				gotPlaceholders = append(gotPlaceholders, string(match[1]))
			}
			slices.Sort(gotPlaceholders)
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
