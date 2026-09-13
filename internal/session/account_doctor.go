package session

import (
	"os"
	"path/filepath"
	"sort"
)

// AccountDirectoryDiagnostic describes configuration, not a live login identity.
// PathState is independent of State: equal missing paths still prove a shared
// configuration target, even though the directory's accessibility is unknown.
type AccountDirectoryDiagnostic struct {
	Name       string   `json:"name"`
	ConfigDir  string   `json:"config_dir"`
	State      string   `json:"state"`
	PathState  string   `json:"path_state"`
	SharedWith []string `json:"shared_with"`
	Reason     string   `json:"reason,omitempty"`
}

type accountDirectoryObservation struct {
	target string
	info   os.FileInfo
}

// DiagnoseClaudeAccountDirectories reads directory metadata only. It neither
// opens credential files nor creates directories or changes configuration.
func DiagnoseClaudeAccountDirectories(config *UserConfig) []AccountDirectoryDiagnostic {
	reports := make([]AccountDirectoryDiagnostic, 0)
	if config == nil {
		return reports
	}
	names := make([]string, 0, len(config.Profiles))
	for name, profile := range config.Profiles {
		if profile.Claude.ConfigDir != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	observations := make([]accountDirectoryObservation, 0, len(names))
	for _, name := range names {
		report := AccountDirectoryDiagnostic{Name: name, ConfigDir: config.GetProfileClaudeConfigDir(name), State: "unknown", PathState: "unknown", SharedWith: []string{}}
		observation := accountDirectoryObservation{}
		if report.ConfigDir == "" {
			report.Reason = "configured path expands to empty"
		} else if !filepath.IsAbs(report.ConfigDir) {
			report.Reason = "relative directory depends on the launch working directory"
		} else {
			// Preserve symlink/.. traversal exactly as the launch environment does.
			// filepath.Abs/Clean would change its target before EvalSymlinks.
			target := report.ConfigDir
			observation.target = target
			resolved, err := filepath.EvalSymlinks(target)
			if err != nil {
				report.Reason = "directory is missing or cannot be resolved"
			} else {
				info, err := os.Stat(resolved)
				if err != nil || !info.IsDir() {
					report.Reason = "target is not an accessible directory"
				} else {
					observation.info = info
					directory, err := os.Open(resolved)
					if err != nil {
						report.Reason = "directory cannot be opened"
					} else {
						closeErr := directory.Close()
						// Keep the literal suffix: cleaning it would bypass the kernel search check.
						_, searchErr := os.Stat(resolved + string(os.PathSeparator) + ".")
						if closeErr != nil || searchErr != nil {
							report.Reason = "directory access is unknown"
						} else {
							report.State = "ok"
							report.PathState = "accessible"
						}
					}
				}
			}
		}
		reports = append(reports, report)
		observations = append(observations, observation)
	}
	for i := range reports {
		for j := i + 1; j < len(reports); j++ {
			a, b := observations[i], observations[j]
			same := a.target != "" && a.target == b.target
			if !same && a.info != nil && b.info != nil {
				same = os.SameFile(a.info, b.info)
			}
			if same {
				reports[i].SharedWith = append(reports[i].SharedWith, reports[j].Name)
				reports[j].SharedWith = append(reports[j].SharedWith, reports[i].Name)
				reports[i].State = "warning"
				reports[j].State = "warning"
			}
		}
	}
	return reports
}
