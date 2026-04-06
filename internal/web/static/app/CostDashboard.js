// CostDashboard.js -- In-app cost dashboard tab (Preact component)
// Replaces the standalone /costs page as an in-app tab with summary cards and Chart.js charts.
import { html } from 'htm/preact'
import { useEffect, useRef, useState } from 'preact/hooks'
import { apiFetch } from './api.js'

const CHART_COLORS = ['#7aa2f7','#bb9af7','#7dcfff','#9ece6a','#e0af68','#f7768e','#73daca','#ff9e64']

function fmt(v) {
  return '$' + (v || 0).toFixed(2)
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
              borderColor: '#7aa2f7',
              backgroundColor: 'rgba(122,162,247,0.1)',
              fill: true,
              tension: 0.3,
            }],
          },
          options: {
            responsive: true,
            plugins: { legend: { display: false } },
            scales: {
              x: { ticks: { color: '#565f89' }, grid: { color: '#1f2335' } },
              y: {
                ticks: { color: '#565f89', callback: v => '$' + v.toFixed(2) },
                grid: { color: '#1f2335' },
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
              backgroundColor: CHART_COLORS.slice(0, mLabels.length),
            }],
          },
          options: {
            responsive: true,
            plugins: {
              legend: {
                position: 'bottom',
                labels: { color: '#c0caf5', font: { size: 11 } },
              },
            },
          },
        })
      } catch (_err) {
        // Charts are optional; summary cards still display
      }
    }

    buildCharts()

    return () => {
      cancelled = true
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
          <div class="text-xs dark:text-tn-muted text-gray-400 mt-1">${summary.today_events} events</div>
        </div>
        <div class="dark:bg-tn-card bg-white rounded-lg p-4">
          <div class="text-xs dark:text-tn-muted text-gray-500 uppercase">This Week</div>
          <div class="text-2xl font-bold dark:text-[#7dcfff] text-teal-600 mt-1">${fmt(summary.week_usd)}</div>
          <div class="text-xs dark:text-tn-muted text-gray-400 mt-1">${summary.week_events} events</div>
        </div>
        <div class="dark:bg-tn-card bg-white rounded-lg p-4">
          <div class="text-xs dark:text-tn-muted text-gray-500 uppercase">This Month</div>
          <div class="text-2xl font-bold dark:text-[#7dcfff] text-teal-600 mt-1">${fmt(summary.month_usd)}</div>
          <div class="text-xs dark:text-tn-muted text-gray-400 mt-1">${summary.month_events} events</div>
        </div>
        <div class="dark:bg-tn-card bg-white rounded-lg p-4">
          <div class="text-xs dark:text-tn-muted text-gray-500 uppercase">Projected</div>
          <div class="text-2xl font-bold dark:text-[#7dcfff] text-teal-600 mt-1">${fmt(summary.projected_usd)}</div>
          <div class="text-xs dark:text-tn-muted text-gray-400 mt-1">based on 7-day avg</div>
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
