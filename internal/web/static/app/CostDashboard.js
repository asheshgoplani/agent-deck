// CostDashboard.js -- In-app cost dashboard tab (Preact component)
// Replaces the standalone /costs page as an in-app tab with summary cards and Chart.js charts.
import { html } from 'htm/preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { apiFetch } from './api.js'

// POL-5 (Phase 9, plan 02): locale-aware currency formatting. Constructed
// once at module load — Intl.NumberFormat is non-trivial to build (reads
// ICU data) and the user's locale does not change during a session. Both
// the summary card fmt() helper and the Chart.js y-axis tick callback
// delegate to this instance so they never drift. Currency stays USD
// regardless of locale (no conversion) — only symbol placement, digit
// grouping, and decimal separator follow navigator.language.
const currencyFormatter = new Intl.NumberFormat(navigator.language, {
  style: 'currency',
  currency: 'USD',
})

function fmt(v) {
  return currencyFormatter.format(v || 0)
}

// readChartTheme reads chart palette CSS variables from the document root.
// Variables are defined in internal/web/static/styles.src.css under :root
// (light) and html.dark (dark override). The MutationObserver wired up
// inside CostDashboard's useEffect re-runs buildCharts() whenever the
// theme class on <html> changes, so this helper produces the live palette.
// Per BUG #13 / UX-02 — replaces the legacy CHART_COLORS constant + isDark
// ternary that hardcoded every chart color in JS.
function readChartTheme() {
  const cs = getComputedStyle(document.documentElement)
  const v = (name, fallback) => (cs.getPropertyValue(name).trim() || fallback)
  return {
    text:        v('--chart-text',         '#6b7280'),
    grid:        v('--chart-grid',         '#e5e7eb'),
    legend:      v('--chart-legend',       '#374151'),
    primary:     v('--chart-primary',      '#2959aa'),
    primaryFill: v('--chart-primary-fill', 'rgba(41, 89, 170, 0.1)'),
    categorical: [
      v('--chart-categorical-1', '#7aa2f7'),
      v('--chart-categorical-2', '#bb9af7'),
      v('--chart-categorical-3', '#7dcfff'),
      v('--chart-categorical-4', '#9ece6a'),
      v('--chart-categorical-5', '#e0af68'),
      v('--chart-categorical-6', '#f7768e'),
      v('--chart-categorical-7', '#73daca'),
      v('--chart-categorical-8', '#ff9e64'),
    ],
  }
}

export function CostDashboard() {
  const [summary, setSummary] = useState(null)
  const [error, setError] = useState(null)
  const [loading, setLoading] = useState(true)

  const dailyCanvasRef = useRef(null)
  const modelCanvasRef = useRef(null)
  const dailyChartRef = useRef(null)
  const modelChartRef = useRef(null)

  // Load summary cards
  useEffect(() => {
    apiFetch('GET', '/api/costs/summary')
      .then(data => {
        setSummary(data)
        setLoading(false)
      })
      .catch(err => {
        setError(err.message || 'Failed to load cost data')
        setLoading(false)
      })
  }, [])

  // Build charts after loading (or when canvases become available)
  useEffect(() => {
    if (loading || error) return
    if (!dailyCanvasRef.current || !modelCanvasRef.current) return

    let cancelled = false

    async function buildCharts() {
      try {
        const [dailyData, modelsData] = await Promise.all([
          apiFetch('GET', '/api/costs/daily?days=30'),
          apiFetch('GET', '/api/costs/models'),
        ])

        if (cancelled) return

        // Destroy old chart instances before creating new ones
        if (dailyChartRef.current) {
          dailyChartRef.current.destroy()
          dailyChartRef.current = null
        }
        if (modelChartRef.current) {
          modelChartRef.current.destroy()
          modelChartRef.current = null
        }

        if (!dailyCanvasRef.current || !modelCanvasRef.current) return

        // Theme-aware chart colors read from CSS custom properties on
        // document.documentElement. The MutationObserver below re-runs
        // buildCharts() whenever the `dark` class toggles, so this read
        // always reflects the active theme without a page reload.
        const t = readChartTheme()

        const dates = dailyData || []
        const labels = dates.map(d => d.date.slice(5))
        const costs = dates.map(d => d.cost_usd)

        dailyChartRef.current = new window.Chart(dailyCanvasRef.current, {
          type: 'line',
          data: {
            labels,
            datasets: [{
              label: 'Daily Cost ($)',
              data: costs,
              borderColor: t.primary,
              backgroundColor: t.primaryFill,
              fill: true,
              tension: 0.3,
            }],
          },
          options: {
            responsive: true,
            plugins: { legend: { display: false } },
            scales: {
              x: { ticks: { color: t.text }, grid: { color: t.grid } },
              y: {
                ticks: { color: t.text, callback: v => currencyFormatter.format(v || 0) },
                grid: { color: t.grid },
              },
            },
          },
        })

        const models = modelsData || {}
        const mLabels = Object.keys(models)
        const mData = Object.values(models)

        modelChartRef.current = new window.Chart(modelCanvasRef.current, {
          type: 'doughnut',
          data: {
            labels: mLabels,
            datasets: [{
              data: mData,
              backgroundColor: t.categorical.slice(0, mLabels.length),
            }],
          },
          options: {
            responsive: true,
            plugins: {
              legend: {
                position: 'bottom',
                labels: { color: t.legend, font: { size: 11 } },
              },
            },
          },
        })
      } catch (_err) {
        // Charts are optional; summary cards still display
      }
    }

    buildCharts()

    // Re-build charts when the theme class on <html> changes (BUG #13 / UX-02).
    // The MutationObserver replaces the previous theme signal dep — the root
    // element class is the source of truth for theme, and reading via
    // getComputedStyle gives Chart.js the new CSS variable values without
    // requiring the parent to re-mount the component.
    const observer = new MutationObserver(() => {
      buildCharts()
    })
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })

    return () => {
      cancelled = true
      observer.disconnect()
    }
  }, [loading, error])

  // Cleanup chart instances on unmount
  useEffect(() => {
    return () => {
      if (dailyChartRef.current) {
        dailyChartRef.current.destroy()
        dailyChartRef.current = null
      }
      if (modelChartRef.current) {
        modelChartRef.current.destroy()
        modelChartRef.current = null
      }
    }
  }, [])

  if (loading) {
    return html`
      <div class="p-4 md:p-6 overflow-y-auto h-full dark:text-tn-fg text-gray-700">
        <p class="text-sm dark:text-tn-muted text-gray-500">Loading cost data...</p>
      </div>
    `
  }

  if (error) {
    return html`
      <div class="p-4 md:p-6 overflow-y-auto h-full dark:text-tn-fg text-gray-700">
        <p class="text-sm dark:text-tn-muted text-gray-500">
          Cost tracking is not enabled. Start agent-deck with cost tracking to see data here.
        </p>
      </div>
    `
  }

  return html`
    <div class="p-sp-16 md:p-sp-24 overflow-y-auto h-full dark:text-tn-fg text-gray-700">

      <!-- Summary cards -->
      <div class="grid grid-cols-2 lg:grid-cols-4 gap-sp-16 mb-sp-24">
        <div class="dark:bg-tn-card bg-white rounded-lg p-4">
          <div class="text-xs dark:text-tn-muted text-gray-500 uppercase">Today</div>
          <div class="text-2xl font-bold dark:text-[#7dcfff] text-teal-600 mt-1">${fmt(summary.today_usd)}</div>
          <div class="text-xs dark:text-tn-muted text-gray-600 mt-1">${summary.today_events} events</div>
        </div>
        <div class="dark:bg-tn-card bg-white rounded-lg p-4">
          <div class="text-xs dark:text-tn-muted text-gray-500 uppercase">This Week</div>
          <div class="text-2xl font-bold dark:text-[#7dcfff] text-teal-600 mt-1">${fmt(summary.week_usd)}</div>
          <div class="text-xs dark:text-tn-muted text-gray-600 mt-1">${summary.week_events} events</div>
        </div>
        <div class="dark:bg-tn-card bg-white rounded-lg p-4">
          <div class="text-xs dark:text-tn-muted text-gray-500 uppercase">This Month</div>
          <div class="text-2xl font-bold dark:text-[#7dcfff] text-teal-600 mt-1">${fmt(summary.month_usd)}</div>
          <div class="text-xs dark:text-tn-muted text-gray-600 mt-1">${summary.month_events} events</div>
        </div>
        <div class="dark:bg-tn-card bg-white rounded-lg p-4">
          <div class="text-xs dark:text-tn-muted text-gray-500 uppercase">Projected</div>
          <div class="text-2xl font-bold dark:text-[#7dcfff] text-teal-600 mt-1">${fmt(summary.projected_usd)}</div>
          <div class="text-xs dark:text-tn-muted text-gray-600 mt-1">based on 7-day avg</div>
        </div>
      </div>

      <!-- Charts -->
      <div class="grid grid-cols-1 lg:grid-cols-3 gap-sp-16 mb-sp-24">
        <div class="lg:col-span-2 dark:bg-tn-card bg-white rounded-lg p-4">
          <div class="text-sm dark:text-tn-muted text-gray-500 uppercase mb-3">Daily Spend (Last 30 Days)</div>
          <canvas ref=${dailyCanvasRef}></canvas>
        </div>
        <div class="dark:bg-tn-card bg-white rounded-lg p-4">
          <div class="text-sm dark:text-tn-muted text-gray-500 uppercase mb-3">Cost by Model</div>
          <canvas ref=${modelCanvasRef}></canvas>
        </div>
      </div>

    </div>
  `
}
