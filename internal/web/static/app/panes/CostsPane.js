// panes/CostsPane.js -- Wraps the existing CostDashboard inside the
// bundle's `.costs` container. CostDashboard is preserved as-is to keep
// chart.js wiring stable.
import { html } from 'htm/preact'
import { CostDashboard } from '../CostDashboard.js'

export function CostsPane() {
  return html`
    <div class="costs">
      <${CostDashboard}/>
    </div>
  `
}
