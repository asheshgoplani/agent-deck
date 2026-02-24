package hub

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// projectsFile is the filename for the project registry.
const projectsFile = "projects.yaml"

// projectsConfig is the YAML structure for the projects file.
type projectsConfig struct {
	Projects []*Project `yaml:"projects"`
}

// ProjectRegistry provides read access to the YAML-based project registry.
type ProjectRegistry struct {
	filePath string
}

// NewProjectRegistry creates a ProjectRegistry that reads from basePath/projects.yaml.
func NewProjectRegistry(basePath string) *ProjectRegistry {
	return &ProjectRegistry{
		filePath: filepath.Join(basePath, projectsFile),
	}
}

// FilePath returns the path to the projects.yaml file.
func (r *ProjectRegistry) FilePath() string {
	return r.filePath
}

// List returns all projects from the registry file.
// Returns an empty slice (not error) if the file does not exist.
func (r *ProjectRegistry) List() ([]*Project, error) {
	data, err := os.ReadFile(r.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read projects file: %w", err)
	}

	var cfg projectsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse projects file: %w", err)
	}

	return cfg.Projects, nil
}
