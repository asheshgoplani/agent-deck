// Exercise the production, installed extension. Node >=22.13 is required.
// With an event, stdin is that event's payload. Without one, stdin is NDJSON,
// one {event, payload} per line, run in order in this process with one
// context. The line {"pause": true} writes {"paused": true} to stdout and
// waits for the next line, so the caller can act between events.
//
// The driver also models the parts of pi a wake socket needs. AGENTDECK_PI_SOCKET
// (and every other variable) reaches the extension untouched. ctx.isIdle() is
// true except during a run. A pi.sendMessage with triggerTurn while idle starts
// the run it asks for, as pi does: before_agent_start{messageType: "custom"},
// then agent_start, turn_start and agent_settled. Whatever before_agent_start
// returns is printed as {"api": "before_agent_start", "returned": <message>}, so
// a caller sees the context the extension would have handed pi; the end of a
// wake's run is {"api": "run_done"}.
import { readFile } from 'node:fs/promises';
import { createInterface } from 'node:readline';
import { stripTypeScriptTypes } from 'node:module';

const [extension, event] = process.argv.slice(2);
const handlers = new Map();
const source = stripTypeScriptTypes(await readFile(extension, 'utf8'));
const { default: install } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
// Future extension revisions can use these APIs. Evidence records actual calls.
const print = (value) => console.log(JSON.stringify(value));
let idle = true;
const api = {
  on(name, callback) { handlers.set(name, callback); },
  sendUserMessage(message, options) { print({ api: 'sendUserMessage', message, options }); },
  sendMessage(message, options) {
    print({ api: 'sendMessage', message, options });
    if (options?.triggerTurn && idle) {
      idle = false;
      setImmediate(() => runWake(message).catch((error) => { console.error(String(error?.stack ?? error)); process.exitCode = 1; }));
    }
  },
};
install(api);
let currentPayload;
const context = { cwd: process.cwd(), isIdle: () => idle, hasPendingMessages: () => false, sessionManager: {
  getSessionId() { return currentPayload?.session_id; },
  getSessionFile() { return currentPayload?.session_file; },
} };
async function dispatch(name, payload) {
  if (!handlers.has(name)) throw new Error(`Installed Pi extension has no ${name} handler`);
  currentPayload = payload;
  const result = await handlers.get(name)(payload, context);
  if (name === 'before_agent_start') print({ api: 'before_agent_start', returned: result?.message ?? null });
  return result;
}
// The run pi starts for a custom message that asked for a turn.
async function runWake(message) {
  const session_id = currentPayload?.session_id;
  try {
    await dispatch('before_agent_start', { type: 'before_agent_start', messageType: 'custom', session_id,
      message: { role: 'custom', ...message, timestamp: Date.now() } });
    // pi raises its lifecycle events whether or not an extension listens.
    for (const [name, extra] of [['agent_start', {}], ['turn_start', {}], ['agent_settled', { aborted: false }]]) {
      if (handlers.has(name)) await dispatch(name, { type: name, session_id, ...extra });
    }
  } finally {
    idle = true;
    print({ api: 'run_done' });
  }
}
async function run(step) {
  if (step.pause) { print({ paused: true }); return; }
  await dispatch(step.event, step.payload);
}
if (event) {
  let input = '';
  for await (const chunk of process.stdin) input += chunk;
  await run({ event, payload: JSON.parse(input) });
} else {
  for await (const line of createInterface({ input: process.stdin })) {
    if (line.trim()) await run(JSON.parse(line));
  }
}
