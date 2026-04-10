package web

import (
	"net/http"

	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
)

func (s *Server) handleSystemStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	stats := sysinfo.Collect()

	resp := map[string]any{}

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
