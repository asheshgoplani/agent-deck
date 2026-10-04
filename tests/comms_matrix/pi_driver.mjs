// Exercise the production, installed extension. Node >=22.13 is required.
import { readFile } from 'node:fs/promises';
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
if (!handlers.has(event)) throw new Error(`Installed Pi extension has no ${event} handler`);
let input = '';
for await (const chunk of process.stdin) input += chunk;
const payload = JSON.parse(input);
const context = { cwd: process.cwd(), sessionManager: {
  getSessionId() { return payload.session_id; },
  getSessionFile() { return payload.session_file; },
} };
await handlers.get(event)(payload, context);
