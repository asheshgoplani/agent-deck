// SystemStats.js -- System stats display for topbar (compact) and empty state (detailed)
import { html } from 'htm/preact'
import { useEffect } from 'preact/hooks'
import { systemStatsSignal, authTokenSignal } from './state.js'

let pollTimer = null

export function startSystemStatsPoll(intervalMs = 5000) {
  if (pollTimer) return
  async function poll() {
    try {
      const headers = { Accept: 'application/json' }
      const token = authTokenSignal.value
      if (token) headers['Authorization'] = 'Bearer ' + token
      const res = await fetch('/api/system/stats', { headers })
      if (res.ok) systemStatsSignal.value = await res.json()
    } catch (_) { /* silent */ }
  }
  poll()
  pollTimer = setInterval(poll, intervalMs)
}

export function stopSystemStatsPoll() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
}

function formatBytes(bytes) {
  if (bytes >= 1 << 30) return (bytes / (1 << 30)).toFixed(1) + 'G'
  if (bytes >= 1 << 20) return (bytes / (1 << 20)).toFixed(1) + 'M'
  if (bytes >= 1 << 10) return Math.round(bytes / (1 << 10)) + 'K'
  return bytes + 'B'
}

function formatBps(bps) { return formatBytes(bps) + '/s' }

function barColor(pct) {
  if (pct >= 90) return 'bg-red-500'
  if (pct >= 70) return 'bg-yellow-500'
  return 'bg-green-500'
}

// Compact inline stats for the topbar
export function SystemStatsCompact() {
  useEffect(() => { startSystemStatsPoll(); return stopSystemStatsPoll }, [])

  const s = systemStatsSignal.value
  if (!s) return null

  const parts = []
  if (s.cpu) parts.push(html`<span class="tabular-nums" title="CPU usage">\u2699 ${Math.round(s.cpu.usage_percent)}%</span>`)
  if (s.memory) parts.push(html`<span class="tabular-nums" title="RAM usage">\u26C1 ${s.memory.used_human}/${s.memory.total_human}</span>`)
  if (s.disk) parts.push(html`<span class="tabular-nums" title="Disk usage">\u25AA ${Math.round(s.disk.usage_percent)}%</span>`)
  if (s.network && (s.network.rx_bytes_per_sec > 0 || s.network.tx_bytes_per_sec > 0)) {
    parts.push(html`<span class="tabular-nums" title="Network">\u21C5 \u2193${s.network.rx_human} \u2191${s.network.tx_human}</span>`)
  }

  if (parts.length === 0) return null

  return html`
    <span class="hidden md:flex items-center gap-2 text-[11px] dark:text-tn-muted/70 text-gray-400 font-mono flex-shrink-0">
      ${parts.map((p, i) => html`${i > 0 && html`<span class="dark:text-tn-muted/30 text-gray-300">\u2502</span>`}${p}`)}
    </span>
  `
}

// Detailed stats grid for empty state dashboard
export function SystemStatsDetailed() {
  useEffect(() => { startSystemStatsPoll(); return stopSystemStatsPoll }, [])

  const s = systemStatsSignal.value
  if (!s) return null

  const rows = []

  if (s.cpu) rows.push({ label: 'CPU', pct: s.cpu.usage_percent, detail: Math.round(s.cpu.usage_percent) + '%' })
  if (s.memory) rows.push({ label: 'RAM', pct: s.memory.usage_percent, detail: s.memory.used_human + ' / ' + s.memory.total_human })
  if (s.disk) rows.push({ label: 'Disk', pct: s.disk.usage_percent, detail: s.disk.used_human + ' / ' + s.disk.total_human })
  if (s.gpu) rows.push({ label: 'GPU', pct: s.gpu.usage_percent, detail: Math.round(s.gpu.usage_percent) + '%' + (s.gpu.name ? ' (' + s.gpu.name + ')' : '') })

  if (rows.length === 0) return null

  return html`
    <div class="w-full max-w-sm mx-auto mt-6 px-4">
      <h3 class="text-xs font-semibold uppercase tracking-wider dark:text-tn-muted/60 text-gray-400 mb-3">System</h3>
      <div class="space-y-2.5">
        ${rows.map(r => html`
          <div class="flex items-center gap-3">
            <span class="text-xs font-medium dark:text-tn-muted text-gray-500 w-10 text-right">${r.label}</span>
            <div class="flex-1 h-2 rounded-full dark:bg-tn-muted/10 bg-gray-100 overflow-hidden">
              <div class="h-full rounded-full transition-all duration-500 ${barColor(r.pct)}" style="width: ${Math.min(100, r.pct)}%"></div>
            </div>
            <span class="text-[11px] tabular-nums dark:text-tn-muted/80 text-gray-400 w-24 text-right font-mono">${r.detail}</span>
          </div>
        `)}
        ${s.load && html`
          <div class="flex items-center gap-3">
            <span class="text-xs font-medium dark:text-tn-muted text-gray-500 w-10 text-right">Load</span>
            <span class="text-[11px] tabular-nums dark:text-tn-muted/80 text-gray-400 font-mono">
              ${s.load.load1.toFixed(2)}${' \u00A0'}${s.load.load5.toFixed(2)}${' \u00A0'}${s.load.load15.toFixed(2)}
            </span>
          </div>
        `}
        ${s.network && html`
          <div class="flex items-center gap-3">
            <span class="text-xs font-medium dark:text-tn-muted text-gray-500 w-10 text-right">Net</span>
            <span class="text-[11px] tabular-nums dark:text-tn-muted/80 text-gray-400 font-mono">
              \u2193 ${s.network.rx_human}${' \u00A0\u00A0'}\u2191 ${s.network.tx_human}
            </span>
          </div>
        `}
      </div>
    </div>
  `
}
