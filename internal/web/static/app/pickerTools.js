// pickerTools.js -- shared new/edit session tool-picker resolution.
//
// pickerTools from GET /api/settings is already filtered server-side
// (hidden_tools + show_only_installed_tools). These helpers apply the
// web-side fallback and edit-dialog "keep current tool" rule.

export const DEFAULT_PICKER_TOOLS = ['claude', 'codex', 'gemini', 'opencode', 'shell']

export const TOOL_DISPLAY_LABELS = {
  codex: 'ChatGPT',
}

export function displayLabelForTool(tool) {
  return TOOL_DISPLAY_LABELS[tool] || tool
}

function uniqueTools(tools) {
  return tools.filter((t, i, arr) => arr.indexOf(t) === i)
}

export function resolveCreateSessionPickerTools(pickerTools) {
  const base = pickerTools.length > 0 ? pickerTools : DEFAULT_PICKER_TOOLS
  return uniqueTools(base)
}

export function resolveEditSessionPickerTools(pickerTools, currentTool) {
  const base = pickerTools.length > 0 ? pickerTools : DEFAULT_PICKER_TOOLS
  const shown = [...base]
  if (currentTool && !shown.includes(currentTool)) {
    shown.push(currentTool)
  }
  return shown
}
