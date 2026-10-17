// Exercise the production, installed extension. Node >=22.13 is required.
// With an event, stdin is that event's payload. Without one, stdin is NDJSON,
// one {event, payload} per line, run in order in this process with one
// context. The line {"pause": true} writes {"paused": true} to stdout and
// waits for the next line, so the caller can act between events.
import { readFile } from 'node:fs/promises';
import { createInterface } from 'node:readline';
import { stripTypeScriptTypes } from 'node:module';

const [extension, event] = process.argv.slice(2);
const handlers = new Map();
const source = stripTypeScriptTypes(await readFile(extension, 'utf8'));
const { default: install } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
// Future extension revisions can use these APIs. Evidence records actual calls.
const api = {
  on(name, callback) { handlers.set(name, callback); },
  sendUserMessage(message, options) { console.log(JSON.stringify({ api: 'sendUserMessage', message, options })); },
  sendMessage(message, options) { console.log(JSON.stringify({ api: 'sendMessage', message, options })); },
};
install(api);
let currentPayload;
const context = { cwd: process.cwd(), sessionManager: {
  getSessionId() { return currentPayload?.session_id; },
  getSessionFile() { return currentPayload?.session_file; },
} };
async function run(step) {
  if (step.pause) { console.log(JSON.stringify({ paused: true })); return; }
  if (!handlers.has(step.event)) throw new Error(`Installed Pi extension has no ${step.event} handler`);
  currentPayload = step.payload;
  await handlers.get(step.event)(step.payload, context);
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
