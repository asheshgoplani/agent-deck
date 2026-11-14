// A pi-like fake for the delivery e2e tests: a raw-tty composer drawn like
// claude_like_fake.py, hosting the REAL agent-deck pi extension against a fake
// of pi's extension API (pi-coding-agent 1.3.0: sendMessage with triggerTurn,
// before_agent_start with messageType user|custom, ctx.isIdle, session_start
// and session_shutdown). It never draws the transcript, so a doorbell cannot
// move anything on screen. Files under <cwd>/.fake:
//   transcript                       path of the user-row transcript (what the tests read)
//   transcript.jsonl.input-state.json  {draft, submits}
//   events.jsonl                     one line per doorbell, injected context and ui access
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const cwd = fs.realpathSync(process.cwd());
const dir = path.join(cwd, ".fake");
fs.mkdirSync(dir, { recursive: true });
const transcript = path.join(dir, "transcript.jsonl");
const statePath = transcript + ".input-state.json";
const eventsPath = path.join(dir, "events.jsonl");
fs.writeFileSync(transcript, "", { flag: "a" });
fs.writeFileSync(path.join(dir, "transcript"), transcript);
const log = (o) => fs.appendFileSync(eventsPath, JSON.stringify(o) + "\n");

let draft = "";
let submits = 0;
let idle = true;
const handlers = {};
const ui = new Proxy({}, { get(_, prop) { if (typeof prop !== "string") return undefined; return () => { log({ ui: prop }); }; } });
// .fake/busy-ctx makes pi report busy to the extension while its hook status says waiting: the stale-status race.
const ctx = { ui, hasUI: true, mode: "tui", cwd, isIdle: () => idle && !fs.existsSync(path.join(dir, "busy-ctx")), hasPendingMessages: () => false,
  sessionManager: { getSessionId: () => "pi-fake-session", getSessionFile: () => transcript } };
const fire = async (type, payload = {}) => { for (const h of handlers[type] ?? []) await h({ type, ...payload }, ctx); };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function runRun(ev) {
  idle = false;
  await fire("agent_start");
  const event = { type: "before_agent_start", systemPrompt: "", systemPromptOptions: {}, ...ev };
  for (const h of handlers.before_agent_start ?? []) {
    const r = await h(event, ctx);
    if (r && r.message) log({ context: r.message.content, customType: r.message.customType });
  }
  await fire("turn_start");
  await sleep(300);
  idle = true;
  await fire("turn_end", { turnIndex: 0, message: { role: "assistant", content: [{ type: "text", text: "ok" }], stopReason: "stop" }, outcome: "completed", messageEntryId: "e" + submits });
  await fire("agent_settled", {});
}

const pi = {
  on(ev, h) { (handlers[ev] ??= []).push(h); return () => {}; },
  sendMessage(msg, options) {
    log({ doorbell: msg.content, details: msg.details, triggerTurn: !!(options && options.triggerTurn) });
    if (idle && options && options.triggerTurn) {
      idle = false;
      setTimeout(() => runRun({ messageType: "custom", message: { role: "custom", customType: msg.customType,
        content: msg.content, display: msg.display, details: msg.details, timestamp: Date.now() } }), 20);
    }
  },
  sendUserMessage() { log({ ui: "sendUserMessage" }); throw new Error("the wake design never calls sendUserMessage"); },
};

const extPath = path.join(process.env.PI_CODING_AGENT_DIR ?? "", "extensions", "agent-deck.ts");
const ext = (await import(pathToFileURL(extPath).href)).default;
ext(pi);

function now() { return new Date().toISOString(); }
function writeState() {
  const tmp = statePath + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify({ id: "idle-1", phase: idle ? "idle" : "busy", busy_bytes: 0, submits, draft }));
  fs.renameSync(tmp, statePath);
}
function render() {
  const cols = process.stdout.columns || 80;
  const div = "─".repeat(cols);
  const draftLines = draft.split("\n");
  const composer = draftLines.map((l, i) => (i === 0 ? "❯ " : "  ") + l);
  const lines = ["⏺ pi is ready.", "", "✻ done", "", div, ...composer, div, "  pi-fake (shift+tab to cycle)"];
  let out = "";
  lines.forEach((l, i) => { out += `\x1b[${i + 1};1H\x1b[2K` + l; });
  const last = draftLines[draftLines.length - 1];
  out += `\x1b[${5 + draftLines.length};${3 + [...last].length + 1}H`;
  process.stdout.write(out);
}
function submit(text) {
  submits++;
  fs.appendFileSync(transcript, JSON.stringify({ type: "user", uuid: "u" + submits, timestamp: now(), message: { role: "user", content: text } }) + "\n");
  log({ submitted: text });
  idle = false;
  fire("input", { text, source: "interactive" }).then(() => runRun({ messageType: "user", prompt: text }));
}

process.stdin.setRawMode(true);
process.stdin.resume();
process.stdin.on("data", (buf) => {
  const chunk = buf.toString("utf8").replace(/\x1b\[[0-9;?]*[A-Za-z~]/g, "");
  for (const ch of chunk) {
    if (ch === "\r") { if (draft.trim()) submit(draft); draft = ""; }
    else if (ch === "\n") draft += "\n";
    else if (ch === "\x7f" || ch === "\b") draft = draft.slice(0, -1);
    else if (ch === "\x03") draft = "";
    else if (ch >= " ") draft += ch;
  }
  render();
  writeState();
});
process.stdout.write("\x1b[?2004h");
await fire("session_start", { reason: "startup" });
render();
writeState();
setInterval(writeState, 200);
