package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestProjectStore(t *testing.T) *ProjectStore {
	t.Helper()
	store, err := NewProjectStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewProjectStore: %v", err)
	}
	return store
}

func TestNewProjectStoreCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "hub")

	store, err := NewProjectStore(basePath)
	if err != nil {
		t.Fatalf("NewProjectStore: %v", err)
	}

	info, err := os.Stat(filepath.Join(basePath, "projects"))
	if err != nil {
		t.Fatalf("projects directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("projects path is not a directory")
	}
	_ = store
}

func TestProjectStoreSaveAndGet(t *testing.T) {
	store := newTestProjectStore(t)

	project := &Project{
		Name:     "agent-deck",
		Repo:     "C0ntr0lledCha0s/agent-deck",
		Path:     "/home/user/projects/agent-deck",
		Keywords: []string{"cli", "agents"},
	}

	if err := store.Save(project); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if project.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be set")
	}
	if project.UpdatedAt.IsZero() {
		t.Fatal("expected UpdatedAt to be set")
	}

	got, err := store.Get("agent-deck")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "agent-deck" {
		t.Fatalf("expected name agent-deck, got %s", got.Name)
	}
	if got.Repo != "C0ntr0lledCha0s/agent-deck" {
		t.Fatalf("expected repo C0ntr0lledCha0s/agent-deck, got %s", got.Repo)
	}
	if got.Path != "/home/user/projects/agent-deck" {
		t.Fatalf("expected path /home/user/projects/agent-deck, got %s", got.Path)
	}
}

func TestProjectStoreSaveRequiresName(t *testing.T) {
	store := newTestProjectStore(t)

	project := &Project{Path: "/some/path"}
	if err := store.Save(project); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestProjectStoreSaveRejectsInvalidName(t *testing.T) {
	store := newTestProjectStore(t)

	for _, name := range []string{".", "..", "foo/bar", "foo\\bar"} {
		project := &Project{Name: name, Path: "/some/path"}
		if err := store.Save(project); err == nil {
			t.Fatalf("expected error for invalid name %q", name)
		}
	}
}

func TestProjectStoreUpdate(t *testing.T) {
	store := newTestProjectStore(t)

	project := &Project{
		Name:     "my-project",
		Path:     "/original/path",
		Keywords: []string{"api"},
	}
	if err := store.Save(project); err != nil {
		t.Fatalf("Save: %v", err)
	}

	firstUpdated := project.UpdatedAt
	time.Sleep(time.Millisecond)

	project.Path = "/updated/path"
	project.Keywords = []string{"api", "backend"}
	if err := store.Save(project); err != nil {
		t.Fatalf("Save update: %v", err)
	}

	got, err := store.Get("my-project")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Path != "/updated/path" {
		t.Fatalf("expected path /updated/path, got %s", got.Path)
	}
	if len(got.Keywords) != 2 {
		t.Fatalf("expected 2 keywords, got %d", len(got.Keywords))
	}
	if !got.UpdatedAt.After(firstUpdated) {
		t.Fatal("expected UpdatedAt to advance on update")
	}
}

func TestProjectStoreList(t *testing.T) {
	store := newTestProjectStore(t)

	for _, name := range []string{"alpha", "beta", "gamma"} {
		project := &Project{Name: name, Path: "/path/" + name}
		if err := store.Save(project); err != nil {
			t.Fatalf("Save %s: %v", name, err)
		}
		time.Sleep(time.Millisecond)
	}

	projects, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}
	// Sorted by creation time (oldest first)
	if projects[0].Name != "alpha" {
		t.Fatalf("expected first project 'alpha', got %s", projects[0].Name)
	}
	if projects[2].Name != "gamma" {
		t.Fatalf("expected last project 'gamma', got %s", projects[2].Name)
	}
}

func TestProjectStoreListEmpty(t *testing.T) {
	store := newTestProjectStore(t)

	projects, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 0 {
		t.Fatalf("expected 0 projects, got %d", len(projects))
	}
}

func TestProjectStoreDelete(t *testing.T) {
	store := newTestProjectStore(t)

	project := &Project{Name: "to-delete", Path: "/path/to-delete"}
	if err := store.Save(project); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete("to-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := store.Get("to-delete")
	if err == nil {
		t.Fatal("expected error after delete, got nil")
	}
}

func TestProjectStoreDeleteNotFound(t *testing.T) {
	store := newTestProjectStore(t)

	err := store.Delete("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent project, got nil")
	}
}

func TestProjectStoreGetNotFound(t *testing.T) {
	store := newTestProjectStore(t)

	_, err := store.Get("nonexistent")
	if err == nil {
		t.Fatal("expected error for non-existent project, got nil")
	}
}

func TestProjectStoreRoundTripContainerConfig(t *testing.T) {
	store := newTestProjectStore(t)

	p := &Project{
		Name:        "myapp",
		Repo:        "org/myapp",
		Path:        "/workspace/myapp",
		Image:       "sandbox-image:latest",
		CPULimit:    2.0,
		MemoryLimit: 2 * 1024 * 1024 * 1024,
		Volumes: []VolumeMount{
			{Host: "/home/user/.ssh", Container: "/tmp/host-ssh", ReadOnly: true},
		},
		Env: map[string]string{"NODE_ENV": "development"},
	}

	if err := store.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Get("myapp")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Image != "sandbox-image:latest" {
		t.Fatalf("expected image sandbox-image:latest, got %s", loaded.Image)
	}
	if loaded.CPULimit != 2.0 {
		t.Fatalf("expected cpuLimit 2.0, got %f", loaded.CPULimit)
	}
	if loaded.MemoryLimit != 2*1024*1024*1024 {
		t.Fatalf("expected memoryLimit 2GB, got %d", loaded.MemoryLimit)
	}
	if len(loaded.Volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(loaded.Volumes))
	}
	if loaded.Volumes[0].Host != "/home/user/.ssh" {
		t.Fatalf("expected volume host /home/user/.ssh, got %s", loaded.Volumes[0].Host)
	}
	if !loaded.Volumes[0].ReadOnly {
		t.Fatal("expected volume to be read-only")
	}
	if loaded.Env["NODE_ENV"] != "development" {
		t.Fatalf("expected env NODE_ENV=development, got %s", loaded.Env["NODE_ENV"])
	}
}

func TestProjectStoreConcurrentAccess(t *testing.T) {
	store := newTestProjectStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := fmt.Sprintf("project-%d", n)
			_ = store.Save(&Project{Name: name, Path: "/path/" + name})
			_, _ = store.Get(name)
			_, _ = store.List()
		}(i)
	}
	wg.Wait()

	projects, err := store.List()
	if err != nil {
		t.Fatalf("List after concurrent ops: %v", err)
	}
	if len(projects) != 20 {
		t.Fatalf("expected 20 projects, got %d", len(projects))
	}
}
