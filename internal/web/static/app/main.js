// main.js -- Preact app entry point
import { render, html } from 'htm/preact'
import { App } from './App.js'

const root = document.getElementById('app-root')
if (root) {
  // AppShell renders a full h-screen layout; ensure mount point has no constraints
  root.style.cssText = 'position:fixed;inset:0;z-index:10;'
  render(html`<${App} />`, root)
}
