package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// dirName is the registry directory under agent-deck's data root.
const dirName = "agents"

// Dir returns the agents registry directory.
//
// The marker is the directory's own name, which is the convention every other
// data directory in the binary follows (inboxes, logs, conductor, locks…).
// Resolution is therefore per-directory: an install that already has
// ~/.agent-deck/agents keeps using it, and a new registry is created under the
// XDG data dir. Deliberately NOT keyed off a sibling marker like "runtime" —
// this deployment has runtime/ in the legacy root while inboxes/, logs/ and
// profiles/ are already XDG, so borrowing another directory's marker would
// strand the registry in whichever root happened to be checked first.
func Dir() (string, error) {
	return agentpaths.EffectiveDataPath(dirName, dirName)
}

// DefinitionDir returns the directory for one named definition.
func DefinitionDir(name string) (string, error) {
	if err := validName(name); err != nil {
		return "", err
	}
	root, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// validName keeps a generated definition name from escaping the registry.
func validName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("agents: empty definition name")
	}
	if trimmed != filepath.Clean(trimmed) || !filepath.IsLocal(trimmed) {
		return fmt.Errorf("agents: unsafe definition name %q", name)
	}
	if strings.ContainsAny(trimmed, `/\`) {
		return fmt.Errorf("agents: definition name %q must not contain a path separator", name)
	}
	return nil
}

// Definition is a loaded post plus its role, as found on disk.
type Definition struct {
	// Name is the registry directory name.
	Name string `json:"name"`
	// Dir is the absolute definition directory.
	Dir  string `json:"dir"`
	Post *Post  `json:"post"`
	Role *Role  `json:"role,omitempty"`
	// LoadError is set when the directory exists but could not be read as a
	// definition. A malformed record is reported, never silently skipped:
	// one bad record must not make the rest of the fleet look smaller than
	// it is.
	LoadError string `json:"load_error,omitempty"`
}

// Load reads one definition directory.
func Load(dir string) (*Definition, error) {
	def := &Definition{Name: filepath.Base(dir), Dir: dir}

	postBytes, err := os.ReadFile(filepath.Join(dir, PostFileName))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", PostFileName, err)
	}
	post, err := ParsePost(postBytes)
	if err != nil {
		return nil, err
	}
	def.Post = post

	roleBytes, err := os.ReadFile(filepath.Join(dir, RoleDirName, RoleFileName))
	switch {
	case err == nil:
		role, parseErr := ParseRole(roleBytes)
		if parseErr != nil {
			return nil, parseErr
		}
		def.Role = role
	case errors.Is(err, fs.ErrNotExist):
		// A post may reference an external role; that is legal.
	default:
		return nil, fmt.Errorf("read %s: %w", RoleFileName, err)
	}

	return def, nil
}

// LoadAll reads every definition in the registry, sorted by name.
//
// A missing registry directory is not an error: it is the zero-config user,
// who must see nothing new. Callers distinguish "no agents" from "could not
// look" by the returned error, never by an empty slice.
func LoadAll() ([]*Definition, error) {
	root, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents registry %q: %w", root, err)
	}

	var defs []*Definition
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, statErr := os.Stat(filepath.Join(dir, PostFileName)); statErr != nil {
			// Not a definition directory. Leave it alone rather than
			// guessing at it.
			continue
		}
		def, loadErr := Load(dir)
		if loadErr != nil {
			defs = append(defs, &Definition{
				Name:      entry.Name(),
				Dir:       dir,
				LoadError: loadErr.Error(),
			})
			continue
		}
		defs = append(defs, def)
	}
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs, nil
}

// Exists reports whether the registry directory is present. A zero-config user
// has no registry, and every new surface keys off this.
func Exists() (bool, error) {
	root, err := Dir()
	if err != nil {
		return false, err
	}
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}

// Write persists a definition directory: agent.yaml, role/role.yaml, the
// copied role bodies, and the adoption report.
//
// It writes only inside the registry. Nothing here touches the source that was
// adopted; that is the whole contract of phase 1.
func Write(dir string, post *Post, role *Role, roleFiles map[string][]byte, report string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create definition dir: %w", err)
	}

	postBytes, err := MarshalPost(post)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(dir, PostFileName), postBytes); err != nil {
		return err
	}

	if role != nil {
		roleDir := filepath.Join(dir, RoleDirName)
		if err := os.MkdirAll(roleDir, 0o700); err != nil {
			return fmt.Errorf("create role dir: %w", err)
		}
		roleBytes, err := MarshalRole(role)
		if err != nil {
			return err
		}
		if err := writeFile(filepath.Join(roleDir, RoleFileName), roleBytes); err != nil {
			return err
		}
		names := make([]string, 0, len(roleFiles))
		for name := range roleFiles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !filepath.IsLocal(name) {
				return fmt.Errorf("role file %q escapes the role directory", name)
			}
			target := filepath.Join(roleDir, name)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return fmt.Errorf("create role subdir for %q: %w", name, err)
			}
			if err := writeFile(target, roleFiles[name]); err != nil {
				return err
			}
		}
	}

	if strings.TrimSpace(report) != "" {
		if err := writeFile(filepath.Join(dir, ReportFileName), []byte(report)); err != nil {
			return err
		}
	}
	return nil
}

// writeFile writes with owner-only permissions. Roles are private by default;
// a definition may encode confidential working methods even though it must
// never hold a credential.
func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	return nil
}

// Digest returns the content digest recorded for role files, so a runtime's
// inputs stay inspectable by digest.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// FingerprintPaths produces a stable fingerprint over the source files an
// adoption read, so a later refresh can show drift without storing the bodies.
func FingerprintPaths(paths []string) string {
	// No source files read means no fingerprint. Returning the digest of an
	// empty stream would look like a real fingerprint and would compare
	// equal across unrelated definitions.
	if len(paths) == 0 {
		return ""
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	hasher := sha256.New()
	for _, path := range sorted {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(hasher, "%s\x00missing\x00", path)
			continue
		}
		fmt.Fprintf(hasher, "%s\x00%d\x00%d\x00", path, info.Size(), info.ModTime().UnixNano())
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}
