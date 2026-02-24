package hub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListProjectsSuccess(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `projects:
  - name: api-service
    path: /home/user/code/api
    keywords:
      - api
      - backend
      - auth
  - name: web-app
    path: /home/user/code/web
    keywords:
      - frontend
      - ui
    container: web-dev
    default_mcps:
      - github
`
	if err := os.WriteFile(filepath.Join(dir, "projects.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("write projects.yaml: %v", err)
	}

	reg := NewProjectRegistry(dir)
	projects, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}

	api := projects[0]
	if api.Name != "api-service" {
		t.Fatalf("expected name api-service, got %s", api.Name)
	}
	if api.Path != "/home/user/code/api" {
		t.Fatalf("expected path /home/user/code/api, got %s", api.Path)
	}
	if len(api.Keywords) != 3 {
		t.Fatalf("expected 3 keywords, got %d", len(api.Keywords))
	}

	web := projects[1]
	if web.Container != "web-dev" {
		t.Fatalf("expected container web-dev, got %s", web.Container)
	}
	if len(web.DefaultMCPs) != 1 || web.DefaultMCPs[0] != "github" {
		t.Fatalf("expected defaultMcps [github], got %v", web.DefaultMCPs)
	}
}

func TestListProjectsFileNotExists(t *testing.T) {
	reg := NewProjectRegistry(t.TempDir())
	projects, err := reg.List()
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if projects != nil {
		t.Fatalf("expected nil projects for missing file, got: %v", projects)
	}
}

func TestListProjectsInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "projects.yaml"), []byte("{{invalid"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg := NewProjectRegistry(dir)
	_, err := reg.List()
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestListProjectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "projects.yaml"), []byte(""), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg := NewProjectRegistry(dir)
	projects, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if projects != nil {
		t.Fatalf("expected nil projects for empty file, got: %v", projects)
	}
}
