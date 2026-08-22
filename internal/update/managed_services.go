package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// commandRun is a test seam used to pin swap -> managed-service restart order.
var commandRun = func(name string, args ...string) error { return exec.Command(name, args...).Run() }
var commandOutput = func(name string, args ...string) ([]byte, error) { return exec.Command(name, args...).Output() }
var managedServicesGOOS = runtime.GOOS

// RestartManagedServices refreshes processes which otherwise keep the old
// executable image. On macOS bootout+bootstrap is mandatory: merely kickstarting
// a BTM-managed job after replacing its binary crash-loops with EX_CONFIG.
func RestartManagedServices() (string, error) {
	switch managedServicesGOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		type launchdJob struct{ path, domain string }
		locations := []launchdJob{
			{filepath.Join(home, "Library", "LaunchAgents"), fmt.Sprintf("gui/%d", os.Getuid())},
			{"/Library/LaunchAgents", fmt.Sprintf("gui/%d", os.Getuid())},
			{"/Library/LaunchDaemons", "system"},
		}
		var jobs []launchdJob
		for _, location := range locations {
			paths, err := filepath.Glob(filepath.Join(location.path, "*.plist"))
			if err != nil {
				return "", err
			}
			for _, p := range paths {
				base := strings.ToLower(filepath.Base(p))
				if strings.HasPrefix(base, "com.agentdeck") || strings.HasPrefix(base, "com.agent-deck") {
					label := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
					if _, err := commandOutput("launchctl", "print", location.domain+"/"+label); err == nil {
						jobs = append(jobs, launchdJob{p, location.domain})
					}
				}
			}
		}
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].path < jobs[j].path })
		if len(jobs) == 0 {
			return "No managed launchd jobs were running; none restarted.", nil
		}
		for _, job := range jobs {
			// bootout can report that a disabled job is not loaded. Bootstrap is
			// still required and its failure is always fatal/loud.
			_ = commandRun("launchctl", "bootout", job.domain, job.path)
			if err := commandRun("launchctl", "bootstrap", job.domain, job.path); err != nil {
				return "", fmt.Errorf("launchd re-bootstrap failed for %s: %w (BTM identity may cause EX_CONFIG until manually bootout+bootstrap'd)", job.path, err)
			}
		}
		return fmt.Sprintf("Re-bootstrapped %d managed launchd job(s).", len(jobs)), nil
	case "linux":
		// Existing agent-deck user units contain agent-deck in their names. Ask
		// systemd for only loaded units; absent systemd is a documented no-op.
		out, err := commandOutput("systemctl", "--user", "list-units", "--type=service", "--state=running", "--no-legend", "--plain")
		if err != nil {
			return "No usable systemd user manager; managed services were not restarted.", nil
		}
		var units []string
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.Contains(fields[0], "agent-deck") {
				units = append(units, fields[0])
			}
		}
		if len(units) == 0 {
			return "No running agent-deck systemd user services; none restarted.", nil
		}
		for _, unit := range units {
			if err := commandRun("systemctl", "--user", "restart", unit); err != nil {
				return "", fmt.Errorf("systemd restart failed for %s: %w", unit, err)
			}
		}
		return fmt.Sprintf("Restarted %d managed systemd user service(s).", len(units)), nil
	default:
		return "Managed-service restart is not applicable on this platform.", nil
	}
}
