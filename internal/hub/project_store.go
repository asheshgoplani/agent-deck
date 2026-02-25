package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// validProjectName returns true if name is safe to use as a filename component.
func validProjectName(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.Contains(name, "/") && !strings.Contains(name, "\\")
}

// ProjectStore provides filesystem JSON-based CRUD for Project records.
// Each project is stored as an individual JSON file (e.g. agent-deck.json)
// under basePath/projects/.
type ProjectStore struct {
	mu         sync.RWMutex
	projectDir string
}

// NewProjectStore creates a ProjectStore backed by the given base directory.
// It creates the projects/ subdirectory if it does not exist.
func NewProjectStore(basePath string) (*ProjectStore, error) {
	projectDir := filepath.Join(basePath, "projects")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		return nil, fmt.Errorf("create project directory: %w", err)
	}
	return &ProjectStore{projectDir: projectDir}, nil
}

// List returns all projects sorted by creation time (oldest first).
func (s *ProjectStore) List() ([]*Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.projectDir)
	if err != nil {
		return nil, fmt.Errorf("read project directory: %w", err)
	}

	var projects []*Project
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		project, err := s.readProjectFile(entry.Name())
		if err != nil {
			continue // skip corrupt files
		}
		projects = append(projects, project)
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].CreatedAt.Before(projects[j].CreatedAt)
	})

	return projects, nil
}

// Get retrieves a single project by name.
func (s *ProjectStore) Get(name string) (*Project, error) {
	if !validProjectName(name) {
		return nil, fmt.Errorf("invalid project name: %q", name)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.readProjectFile(name + ".json")
}

// Save persists a project. Name is required and used as the file key.
// UpdatedAt is always set to now. CreatedAt is set on first save.
func (s *ProjectStore) Save(project *Project) error {
	if !validProjectName(project.Name) {
		return fmt.Errorf("invalid project name: %q", project.Name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if project.CreatedAt.IsZero() {
		project.CreatedAt = time.Now().UTC()
	}
	project.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project: %w", err)
	}

	path := filepath.Join(s.projectDir, project.Name+".json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write project file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename project file: %w", err)
	}

	return nil
}

// Delete removes a project by name.
func (s *ProjectStore) Delete(name string) error {
	if !validProjectName(name) {
		return fmt.Errorf("invalid project name: %q", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.projectDir, name+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("project not found: %s", name)
		}
		return fmt.Errorf("delete project file: %w", err)
	}
	return nil
}

func (s *ProjectStore) readProjectFile(filename string) (*Project, error) {
	data, err := os.ReadFile(filepath.Join(s.projectDir, filename))
	if err != nil {
		return nil, fmt.Errorf("read project file %s: %w", filename, err)
	}
	var project Project
	if err := json.Unmarshal(data, &project); err != nil {
		return nil, fmt.Errorf("unmarshal project %s: %w", filename, err)
	}
	return &project, nil
}
