package health

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Distribution struct {
	Min   float64 `json:"min"`
	P50   float64 `json:"p50"`
	Max   float64 `json:"max"`
	Count int     `json:"count"`
}
type ProcessReport struct {
	Latest Sample                  `json:"latest"`
	Stats  map[string]Distribution `json:"stats"`
	Flags  []string                `json:"flags"`
}
type Budgets struct {
	StatusPassMS        float64 `json:"status_pass_ms_exclusive"`
	OpenFDs             int     `json:"open_fds_exclusive"`
	TmuxCallsPerSession int     `json:"tmux_calls_per_session"`
	RemotePollMS        float64 `json:"remote_poll_ms_exclusive"`
}
type Summary struct {
	Budgets   Budgets         `json:"budgets"`
	Version   int             `json:"version"`
	Since     time.Time       `json:"since"`
	Processes []ProcessReport `json:"processes"`
	Flags     []string        `json:"flags"`
}

func numeric(s Sample) map[string]float64 {
	m := map[string]float64{}
	if s.CPUPercent != nil {
		m["cpu_percent"] = *s.CPUPercent
	}
	if s.RSSBytes != nil {
		m["rss_bytes"] = float64(*s.RSSBytes)
	}
	if s.OpenFDs != nil {
		m["open_fds"] = float64(*s.OpenFDs)
	}
	if s.Goroutines != nil {
		m["goroutines"] = float64(*s.Goroutines)
	}
	if s.HookFiles != nil {
		m["hook_files"] = float64(*s.HookFiles)
	}
	if s.StatusPassMS != nil {
		m["status_pass_ms"] = *s.StatusPassMS
	}
	if s.Sessions != nil {
		m["session_count"] = float64(*s.Sessions)
	}
	if s.TmuxCalls != nil {
		m["tmux_calls"] = float64(*s.TmuxCalls)
	}
	if s.DBQueryMS != nil {
		m["session_list_db_ms"] = *s.DBQueryMS
	}
	for name, r := range s.Remotes {
		m["remote."+name+".latency_ms"] = r.LatencyMS
	}
	return m
}

// Report reads only bounded regular health files, tolerating a writer's partial
// final line. Missing and corrupt data are explicitly reported as unknown.
func Report(dir string, since time.Duration) (Summary, error) {
	now := time.Now().UTC()
	result := Summary{Budgets: Budgets{float64(StatusPassBudget / time.Millisecond), DescriptorBudget, 2, float64(RemotePollBudget / time.Millisecond)}, Version: 1, Since: now.Add(-since), Processes: []ProcessReport{}, Flags: []string{}}
	if since <= 0 {
		return result, fmt.Errorf("since must be a positive duration")
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		result.Flags = append(result.Flags, "runtime health unknown: no samples")
		return result, nil
	}
	if err != nil {
		return result, err
	}
	grouped := map[string][]Sample{}
	incomplete := false
	for _, entry := range entries {
		if !healthFile(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxFileBytes {
			incomplete = true
			continue
		}
		file, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			incomplete = true
			continue
		}
		reader := bufio.NewReader(io.LimitReader(file, maxFileBytes+1))
		for {
			line, readErr := reader.ReadBytes('\n')
			if len(line) > 0 {
				if line[len(line)-1] != '\n' {
					incomplete = true
				} else {
					var s Sample
					if json.Unmarshal(line, &s) != nil || s.Version != 1 || s.Timestamp.IsZero() || s.StartedAt.IsZero() || s.PID <= 0 || s.Role == "" {
						incomplete = true
					} else if !s.Timestamp.Before(result.Since) && !s.Timestamp.After(now) {
						key := fmt.Sprintf("%s/%d/%s", s.Role, s.PID, s.StartedAt.Format(time.RFC3339Nano))
						grouped[key] = append(grouped[key], s)
					}
				}
			}
			if readErr != nil {
				if readErr != io.EOF {
					incomplete = true
				}
				break
			}
		}
		_ = file.Close()
	}
	if incomplete {
		result.Flags = append(result.Flags, "some health data is unknown: incomplete or corrupt samples")
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		samples := grouped[key]
		sort.Slice(samples, func(i, j int) bool { return samples[i].Timestamp.Before(samples[j].Timestamp) })
		p := ProcessReport{Latest: samples[len(samples)-1], Stats: map[string]Distribution{}, Flags: []string{}}
		values := map[string][]float64{}
		flagSet := map[string]bool{}
		add := func(s string) { flagSet[s] = true }
		for _, s := range samples {
			for name, value := range numeric(s) {
				values[name] = append(values[name], value)
			}
			if s.StatusPassMS != nil && *s.StatusPassMS >= float64(StatusPassBudget/time.Millisecond) {
				add("status pass exceeds 250 ms budget")
			}
			if s.OpenFDs != nil && *s.OpenFDs >= DescriptorBudget {
				add("descriptor count exceeds 512 budget")
			}
			if s.Sessions != nil && s.TmuxCalls != nil && *s.TmuxCalls > int64(2*(*s.Sessions)) {
				add("tmux calls exceed twice the session count")
			}
			for name, r := range s.Remotes {
				if r.LatencyMS >= float64(RemotePollBudget/time.Millisecond) {
					add(fmt.Sprintf("remote %s is slow", strconv.QuoteToASCII(name)))
				}
				if r.Outcome != "ok" {
					add(fmt.Sprintf("remote %s poll outcome: %s", strconv.QuoteToASCII(name), strconv.QuoteToASCII(r.Outcome)))
				}
			}
		}
		if now.Sub(p.Latest.Timestamp) > 2*time.Minute {
			add("stale: latest sample is older than 2 minutes")
		}
		if fds := values["open_fds"]; len(fds) > 1 && fds[len(fds)-1] > fds[0] {
			add("descriptor growth observed")
		}
		for name, v := range values {
			sort.Float64s(v)
			median := v[len(v)/2]
			if len(v)%2 == 0 {
				median = (v[len(v)/2-1] + median) / 2
			}
			p.Stats[name] = Distribution{v[0], median, v[len(v)-1], len(v)}
		}
		for flag := range flagSet {
			p.Flags = append(p.Flags, flag)
		}
		sort.Strings(p.Flags)
		result.Processes = append(result.Processes, p)
	}
	if len(result.Processes) == 0 {
		result.Flags = append(result.Flags, "runtime health unknown: no samples in requested period")
	}
	return result, nil
}
func Format(s Summary) string {
	var b strings.Builder
	b.WriteString("Runtime health\n")
	fmt.Fprintf(&b, "  Budgets: status pass <%.0f ms; descriptors <%d; tmux calls <=%d per session; remote poll <%.0f ms\n", s.Budgets.StatusPassMS, s.Budgets.OpenFDs, s.Budgets.TmuxCallsPerSession, s.Budgets.RemotePollMS)
	for _, flag := range s.Flags {
		fmt.Fprintf(&b, "  %s\n", flag)
	}
	for _, p := range s.Processes {
		fmt.Fprintf(&b, "  %s pid %d, latest %s\n", strconv.QuoteToASCII(p.Latest.Role), p.Latest.PID, p.Latest.Timestamp.Format(time.RFC3339))
		names := []string{"cpu_percent", "rss_bytes", "open_fds", "goroutines", "hook_files", "status_pass_ms", "session_count", "tmux_calls", "session_list_db_ms"}
		for name := range p.Stats {
			if strings.HasPrefix(name, "remote.") {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		latest := numeric(p.Latest)
		for _, name := range names {
			if d, ok := p.Stats[name]; ok {
				value := "unknown"
				if v, ok := latest[name]; ok {
					value = fmt.Sprintf("%.2f", v)
				}
				fmt.Fprintf(&b, "    %s latest: %s; min/p50/max: %.2f / %.2f / %.2f\n", strconv.QuoteToASCII(name), value, d.Min, d.P50, d.Max)
			} else {
				fmt.Fprintf(&b, "    %s: unknown\n", strconv.QuoteToASCII(name))
			}
		}
		remoteNames := make([]string, 0, len(p.Latest.Remotes))
		for name := range p.Latest.Remotes {
			remoteNames = append(remoteNames, name)
		}
		sort.Strings(remoteNames)
		for _, name := range remoteNames {
			fmt.Fprintf(&b, "    remote %s latest outcome: %s\n", strconv.QuoteToASCII(name), strconv.QuoteToASCII(p.Latest.Remotes[name].Outcome))
		}
		for _, flag := range p.Flags {
			fmt.Fprintf(&b, "    %s\n", flag)
		}
	}
	return b.String()
}
