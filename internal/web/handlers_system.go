package web

import (
	"net/http"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
	"golang.org/x/sync/singleflight"
)

// systemStatsMaxAge matches the web UI's 5 s stats poll.
const systemStatsMaxAge = 5 * time.Second

// systemStatsCache collects system stats on request and shares the result,
// the same way tmux's CapturePane cache does. A sample younger than
// systemStatsMaxAge is served as is. Otherwise one collection runs in the
// requesting goroutine and concurrent requests wait for its result, so an
// idle server runs no probes and many polling tabs run at most one at a time.
type systemStatsCache struct {
	mu        sync.RWMutex
	stats     sysinfo.Stats
	collected time.Time
	sf        singleflight.Group
}

func (c *systemStatsCache) get() sysinfo.Stats {
	if stats, ok := c.fresh(); ok {
		return stats
	}
	v, _, _ := c.sf.Do("collect", func() (any, error) {
		// Double-check: another request may have just collected.
		if stats, ok := c.fresh(); ok {
			return stats, nil
		}
		stats := sysinfo.Collect()
		c.mu.Lock()
		c.stats, c.collected = stats, time.Now()
		c.mu.Unlock()
		return stats, nil
	})
	stats, _ := v.(sysinfo.Stats)
	return stats
}

func (c *systemStatsCache) fresh() (sysinfo.Stats, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stats, !c.collected.IsZero() && time.Since(c.collected) < systemStatsMaxAge
}

func (s *Server) handleSystemStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	resp := map[string]any{}
	if s.systemStats == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	stats := s.systemStats.get()

	if stats.CPU.Available {
		resp["cpu"] = map[string]any{
			"usage_percent": stats.CPU.UsagePercent,
		}
	}
	if stats.Memory.Available {
		resp["memory"] = map[string]any{
			"used_bytes":    stats.Memory.UsedBytes,
			"total_bytes":   stats.Memory.TotalBytes,
			"usage_percent": stats.Memory.UsagePercent,
			"used_human":    sysinfo.FormatBytes(stats.Memory.UsedBytes),
			"total_human":   sysinfo.FormatBytes(stats.Memory.TotalBytes),
		}
	}
	if stats.Disk.Available {
		resp["disk"] = map[string]any{
			"used_bytes":    stats.Disk.UsedBytes,
			"total_bytes":   stats.Disk.TotalBytes,
			"usage_percent": stats.Disk.UsagePercent,
			"used_human":    sysinfo.FormatBytes(stats.Disk.UsedBytes),
			"total_human":   sysinfo.FormatBytes(stats.Disk.TotalBytes),
		}
	}
	if stats.Load.Available {
		resp["load"] = map[string]any{
			"load1":  stats.Load.Load1,
			"load5":  stats.Load.Load5,
			"load15": stats.Load.Load15,
		}
	}
	if stats.GPU.Available {
		resp["gpu"] = map[string]any{
			"usage_percent": stats.GPU.UsagePercent,
			"name":          stats.GPU.Name,
		}
	}
	if stats.Network.Available {
		resp["network"] = map[string]any{
			"rx_bytes_per_sec": stats.Network.RxBytesPerSec,
			"tx_bytes_per_sec": stats.Network.TxBytesPerSec,
			"rx_human":         sysinfo.FormatBytesPerSec(stats.Network.RxBytesPerSec),
			"tx_human":         sysinfo.FormatBytesPerSec(stats.Network.TxBytesPerSec),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func init() {
	registerFeatureRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("/api/system/stats", s.handleSystemStats)
	})
}
