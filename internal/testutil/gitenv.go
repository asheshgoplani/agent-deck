package testutil

import (
	"os"
	"strings"
)

var gitRepoEnvKeys = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_COMMON_DIR",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_PREFIX",
}

// UnsetGitRepoEnv removes VCS repository-routing env vars from the current process.
// This prevents subprocess git commands from accidentally targeting the caller's repo.
func UnsetGitRepoEnv() {
	for _, k := range gitRepoEnvKeys {
		_ = os.Unsetenv(k)
	}
}

// CleanGitEnv returns a copy of base with VCS repository-routing env vars removed.
func CleanGitEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		skip := false
		for _, k := range gitRepoEnvKeys {
			if strings.HasPrefix(kv, k+"=") {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}
