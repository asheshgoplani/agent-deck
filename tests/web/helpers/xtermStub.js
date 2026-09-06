// helpers/xtermStub.js -- Stand-in for the @xterm packages under Vitest.
//
// The browser resolves @xterm/* through the import map in index.html to
// /static/vendor/*.mjs; that map does not exist in Node. Component modules
// that import TerminalPanel.js (directly or via paneRegistry.js) only need
// the specifiers to resolve, so alias them here. No test drives a terminal.
export class Terminal {
  constructor() { this.options = {} }
  loadAddon() {}
  open() {}
  write() {}
  dispose() {}
  onData() { return { dispose() {} } }
  onResize() { return { dispose() {} } }
}
export class FitAddon { fit() {} dispose() {} }
export class WebglAddon { dispose() {} }
