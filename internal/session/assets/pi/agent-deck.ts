// AGENTDECK PI HOOK EXTENSION v5
//
// Managed by `agent-deck pi-hooks install`. Local edits are overwritten on the
// next install/upgrade, and `agent-deck pi-hooks uninstall` deletes this file.
//
// It forwards pi's lifecycle events to `agent-deck hook-handler` on stdin so a
// pi session's light is driven by real events instead of pane-regex guesses.
// Event mapping (agent-deck side, cmd/agent-deck/hook_handler.go):
//
//   session_start    -> waiting   (at the prompt)
//   turn_start       -> running   (the agent is working; after an injection it
//                                  also carries injected_token, the receipt)
//   turn_end         -> waiting   (back at the prompt)
//   session_shutdown -> dead      (the session runtime is being torn down)
//
// agent_settled carries no status. pi fires it once per run, after retries
// and compaction, when it will not continue on its own. The extension then
// reports the run's last turn_end (session id, entry id, assistant text) with
// the last input as the prompt; hook-handler turns that into one Comms Ledger
// turn. A run that ended without text (tool-only, aborted, failed) is reported
// without it.
//
// turn_start, turn_end and agent_settled also carry `settles: true` (this
// extension sends agent_settled, so a turn_end that has not settled is a status
// edge only) and `run_id`, a token minted at agent_start, so a late settle can
// never attach to a newer run's turn_end.
//
// When AGENTDECK_PI_SOCKET names a path (set at launch for pi 1.2.0 and
// later), the extension also serves a wake socket there for the daemon. One
// connection carries one NDJSON frame {"v":1,"id":"<nonce>","line":"<text>",
// "ids":["<record id>",...]} and gets one reply {"id":"<nonce>","state":...}:
//
//   submitted  pi started the run our doorbell asked for (before_agent_start
//              carried the nonce)
//   queued     handed to pi, no run start seen within 1.5 s of the frame
//   busy, duplicate, bad_request
//              refused, with ok:false and notSent:true; nothing was sent
//
// The socket is bound at a private path and renamed over the public one (last
// starter wins), and removed only by the process whose socket it is. A
// connection that has not delivered a frame 2 s after it connected is dropped,
// and the line is held to 900 B with control characters stripped.
//
// An unparseable, unended or over-cap frame is destroyed with no reply. The
// doorbell is a custom message (pi.sendMessage, triggerTurn), never typed
// input, so it cannot collide with a draft in the editor. On before_agent_start
// the extension asks hook-handler for context and returns its stdout as the
// run's injected message; a doorbell's run also carries wake_nonce.
//
// Only the pi agent-deck launched in the pane serves the socket, and only the
// pi that serves it takes the ledger's context. Its extension puts its own pid
// in AGENTDECK_PI_HOST_PID in process.env, which everything that pi starts
// inherits (pi's bash tool spawns with process.env), so a pi started inside
// the pane (a subagent's `pi -p`, a script) finds another pid there: it is a
// guest of the pane, serves nothing, and marks every payload pane_guest. A
// local launch sets the variable empty, so a launched or restarted pi is never
// a guest. It is a pid, not a flag, because /reload re-evaluates this file in
// the same process, which must still find itself the host.
//
// While the socket at AGENTDECK_PI_SOCKET is the one this runtime bound, every
// payload also carries pane_host. For a parent whose launch exported the
// socket, hook-handler gives context, applies receipts and spools run starts
// only for a payload so marked. A pi that is not a guest but serves nothing is
// not the host either: one in another tmux pane or window of the session (it
// has the session's AGENTDECK_INSTANCE_ID but not the launch's socket), an
// older pi whose path a restarted one took over, or a launched pi whose bind
// failed. Its run starts are given none of the parent's records; they wait for
// the pi that serves.
//
// It is a no-op outside agent-deck: without AGENTDECK_INSTANCE_ID in the
// environment nothing is spawned and no handler is registered. pi awaits
// extension handlers in order, and each emit is awaited too, so back-to-back
// events (e.g. session_shutdown immediately followed by session_start on
// /new, /resume, /fork, /clone, /reload) are delivered to hook-handler in the
// order pi fired them instead of racing as two concurrent processes (#2222).
// A missing or failing agent-deck binary still can never block or crash a
// turn: emit resolves on the child's close/error, or a short timeout if the
// process wedges, so a stuck handler cannot hang pi indefinitely.

import { spawn } from "node:child_process";
import fs from "node:fs";
import crypto from "node:crypto";
import net from "node:net";
import path from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const INSTANCE_ID = (process.env.AGENTDECK_INSTANCE_ID ?? "").trim();
const SOCKET = (process.env.AGENTDECK_PI_SOCKET ?? "").trim();
const HOST_PID_ENV = "AGENTDECK_PI_HOST_PID";
const EMIT_TIMEOUT_MS = 2000;
// before_agent_start's answer becomes the run's injected context, so it gets
// longer than a status emit: a slow handler must not silently drop it.
const INJECT_TIMEOUT_MS = 10 * 1000;
const WAKE_TYPE = "agent-deck-wake";
const CONTEXT_TYPE = "agent-deck-context";
// A frame is one short line; the daemon sends at most 900 B of line.
const MAX_FRAME_BYTES = 4096;
// The extension answers queued this long after the frame arrives, inside the
// daemon's 2 s reply window.
const QUEUED_AFTER_MS = 1500;
// The same record ids are not sent twice inside the ledger's own window.
const DUPLICATE_WINDOW_MS = 90 * 1000;
const MAX_HOOK_OUTPUT_BYTES = 64 * 1024;
// The wake line is at most 900 B from the daemon; the extension holds the same
// limit, since anyone who can open the socket can start a model turn with it.
const MAX_LINE_BYTES = 900;
// A frame arrives in one write; a client that has not delivered one this long
// after it connected, however it trickles, or that is not one of a few, is dropped.
const CONN_DEADLINE_MS = 2000;
const MAX_CONNECTIONS = 8;
// Record ids kept for the duplicate window; the oldest go first.
const MAX_SEEN_IDS = 4096;
// hook-handler reads at most 1 MiB; the daemon keeps 16 KiB of a turn's text.
const MAX_TEXT_BYTES = 16 * 1024;

// tailBytes keeps the last max bytes of text: a worker's completion sentinel
// is the last line of its answer.
function tailBytes(text: string, max: number): string {
  const buf = Buffer.from(text, "utf8");
  if (buf.length <= max) return text;
  let start = buf.length - max;
  while (start < buf.length && (buf[start] & 0xc0) === 0x80) start++;
  return buf.subarray(start).toString("utf8");
}

function assistantText(message: any): string {
  const parts = Array.isArray(message?.content) ? message.content : [];
  return parts
    .filter((part: any) => part?.type === "text" && typeof part.text === "string")
    .map((part: any) => part.text)
    .join("\n");
}

// headBytes keeps the first max bytes of buf, never ending inside a character.
function headBytes(buf: Buffer, max: number): Buffer {
  if (buf.length <= max) return buf;
  let end = max;
  while (end > 0 && (buf[end] & 0xc0) === 0x80) end--;
  return buf.subarray(0, end);
}

// run delivers one event to hook-handler and resolves with its trimmed stdout
// (null when it fails, exits non-zero, or wedges past timeoutMs). The child leads
// its own process group so a timeout can kill it and anything it started: left
// alive it could commit ledger changes after its answer was thrown away.
function run(event: string, fields: Record<string, unknown> = {}, timeoutMs = EMIT_TIMEOUT_MS): Promise<string | null> {
  return new Promise((resolve) => {
    try {
      const payload = JSON.stringify({
        hook_event_name: event,
        cwd: process.cwd(),
        source: "pi",
        ...fields,
      });
      const child = spawn("agent-deck", ["hook-handler"], {
        stdio: ["pipe", "pipe", "ignore"],
        detached: true,
        // hook-handler bounds itself by this, so it still ends if pi dies and
        // takes the timer below with it; 500 ms shorter, so a hook that prints
        // at its own bound is not discarded by ours.
        env: { ...process.env, AGENTDECK_HOOK_DEADLINE_MS: String(timeoutMs - 500) },
      });
      const chunks: Buffer[] = [];
      let size = 0;
      child.stdout.on("data", (chunk: Buffer) => {
        // Keep a little past the cap so a cut can tell whether it lands inside a character.
        const room = MAX_HOOK_OUTPUT_BYTES + 4 - size;
        if (room > 0) chunks.push(chunk.subarray(0, room));
        size += chunk.length;
      });
      const timer = setTimeout(() => {
        try {
          process.kill(-child.pid!, "SIGKILL");
        } catch {
          child.kill("SIGKILL");
        }
        resolve(null);
      }, timeoutMs);
      timer.unref();
      const done = (out: string | null) => {
        clearTimeout(timer);
        resolve(out);
      };
      // A missing binary surfaces as an "error" event, and a handler that
      // exited early surfaces as EPIPE on stdin. Swallow both: status reporting
      // must never take the session down with it. The timeout guarantees run
      // always settles even if the child wedges without exiting.
      child.on("error", () => done(null));
      child.on("close", (code) =>
        done(code === 0 ? headBytes(Buffer.concat(chunks), MAX_HOOK_OUTPUT_BYTES).toString("utf8").trim() : null),
      );
      child.stdin.on("error", () => {});
      child.stdin.end(payload);
    } catch {
      resolve(null);
    }
  });
}

function emit(event: string, fields: Record<string, unknown> = {}): Promise<void> {
  return run(event, fields).then(() => {});
}

// reply writes the one reply and then destroys the connection once it is
// flushed. end() alone only half-closes: the socket stays open, and counted
// against MAX_CONNECTIONS, until the client closes its side, so a client that
// never does would hold a slot for the life of the pi session.
function reply(conn: net.Socket, body: object): void {
  try {
    if (!conn.destroyed) conn.end(JSON.stringify(body) + "\n", () => conn.destroy());
  } catch {
    // The client went away; there is nobody left to tell.
  }
}

type Wake = { nonce: string; ids: string[]; conn: net.Socket; timer: NodeJS.Timeout };

// serveWakeSocket serves sockPath for the life of each pi session. The caller
// wires start to session_start, stop to session_shutdown and rang to
// before_agent_start: the matrix driver keeps one callback per event name, so
// each event has exactly one pi.on. All state is per call so /reload, which
// builds a fresh runtime, starts clean.
function serveWakeSocket(pi: ExtensionAPI, sockPath: string) {
  let server: net.Server | null = null;
  let ctx: any;
  const conns = new Set<net.Socket>();
  // The one doorbell whose fate is not yet known.
  let pending: Wake | null = null;
  // Record ids this runtime has seen submitted, and when (monotonic clock, so a
  // wall-clock jump neither ends a window early nor holds one open). Insertion
  // order is time order, which is what pruning and eviction rely on.
  const submitted = new Map<string, number>();
  // The socket file this runtime bound, so stop removes only that one.
  let owned: { dev: bigint; ino: bigint } | null = null;

  const warn = (msg: string) => console.error("agent-deck: " + msg);

  const refuse = (conn: net.Socket, id: string, state: string) =>
    reply(conn, { id, ok: false, state, notSent: true });

  // settle answers the pending doorbell; it clears the timer, so the timer and
  // the run start cannot both answer.
  const settle = (wake: Wake, state: "submitted" | "queued") => {
    pending = null;
    clearTimeout(wake.timer);
    if (state === "submitted") {
      const now = performance.now();
      for (const id of wake.ids) {
        submitted.delete(id);
        submitted.set(id, now);
      }
      for (const id of submitted.keys()) {
        if (submitted.size <= MAX_SEEN_IDS) break;
        submitted.delete(id);
      }
    }
    reply(wake.conn, { id: wake.nonce, state });
  };

  const isDuplicate = (ids: string[]): boolean => {
    const now = performance.now();
    for (const [id, at] of submitted) {
      if (now - at < DUPLICATE_WINDOW_MS) break;
      submitted.delete(id);
    }
    return ids.every((id) => submitted.has(id));
  };

  const handle = (conn: net.Socket, text: string) => {
    let req: any;
    try {
      req = JSON.parse(text);
    } catch {
      conn.destroy();
      return;
    }
    // Without an id there is nothing to echo, so there is nobody to answer.
    if (typeof req?.id !== "string" || req.id === "") {
      conn.destroy();
      return;
    }
    const id: string = req.id;
    const ids = req.ids;
    const usable =
      req.v === 1 &&
      typeof req.line === "string" &&
      req.line !== "" &&
      Array.isArray(ids) &&
      ids.length > 0 &&
      ids.every((x: unknown) => typeof x === "string" && x !== "");
    if (!usable || Buffer.byteLength(req.line) > MAX_LINE_BYTES) return refuse(conn, id, "bad_request");
    // C0 controls other than tab, DEL and the C1 controls never reach the model.
    const line: string = req.line.replace(/[\x00-\x08\x0a-\x1f\x7f-\x9f]/g, "");
    if (line === "") return refuse(conn, id, "bad_request");
    // The extension re-checks idleness at delivery: the daemon's view can be stale.
    if (pending || !ctx.isIdle()) return refuse(conn, id, "busy");
    if (isDuplicate(ids)) return refuse(conn, id, "duplicate");

    const wake: Wake = { nonce: id, ids, conn, timer: setTimeout(() => settle(wake, "queued"), QUEUED_AFTER_MS) };
    wake.timer.unref();
    pending = wake;
    // Registered before the call: pi raises before_agent_start from inside it.
    pi.sendMessage(
      { customType: WAKE_TYPE, content: line, display: true, details: { nonce: id, ids } },
      { triggerTurn: true },
    );
  };

  const onConnection = (conn: net.Socket) => {
    conns.add(conn);
    conn.on("close", () => {
      conns.delete(conn);
      if (pending?.conn === conn) {
        clearTimeout(pending.timer);
        pending = null;
      }
    });
    conn.on("error", () => {});
    conn.setEncoding("utf8");
    const deadline = setTimeout(() => conn.destroy(), CONN_DEADLINE_MS);
    deadline.unref();
    conn.on("close", () => clearTimeout(deadline));
    let buf = "";
    let taken = false;
    conn.on("data", (chunk: string) => {
      if (taken) return;
      buf += chunk;
      if (Buffer.byteLength(buf) > MAX_FRAME_BYTES) {
        taken = true;
        conn.destroy();
        return;
      }
      const nl = buf.indexOf("\n");
      if (nl < 0) return;
      taken = true;
      // The frame is in; the reply wait has its own timer.
      clearTimeout(deadline);
      try {
        handle(conn, buf.slice(0, nl));
      } catch {
        conn.destroy();
      }
    });
  };

  // A starter binds at a private path in the socket's directory and renames it
  // over the public one, so the public path never holds anything half-made and
  // whatever was there is replaced atomically. Only the launched pi starts (a
  // guest of the pane never serves), so a socket already at the path is a
  // predecessor's: an earlier pi of this session that died, or one still
  // shutting down after a restart. The launched pi takes the path over, and
  // the predecessor's listener is left unreachable and inert. Two starters
  // both rename, and the last wins; the other's stop leaves the winner's socket
  // alone because the dev/ino differ. The private name is never the public
  // one, so the unlink Server.close does by name can only ever hit a file that
  // is already gone. One window remains: stop checks dev/ino and then removes
  // the path, and a socket renamed in between is removed with it, so a pi that
  // takes over inside a predecessor's stop is left without a socket: the daemon
  // rings it nothing until it restarts, and its records ride its run starts.
  // The name carries the owner's pid and 8 random characters, so starters do
  // not collide on it.
  const privateName = () => "p" + process.pid + "-" + crypto.randomBytes(6).toString("hex").slice(0, 8);

  // A starter killed between bind and rename leaves its private socket behind.
  // Only a socket with that name shape and a dead pid is removed.
  const sweepDead = (dir: string) => {
    for (const name of fs.readdirSync(dir)) {
      const m = /^p(\d+)-[0-9a-z]{8,}$/.exec(name);
      if (!m) continue;
      const file = path.join(dir, name);
      try {
        if (!fs.lstatSync(file).isSocket()) continue;
        process.kill(Number(m[1]), 0);
      } catch (e: any) {
        if (e.code === "ESRCH") fs.rmSync(file, { force: true });
      }
    }
  };

  const start = async (_event?: any, context?: any) => {
    ctx = context;
    if (server) return;
    let priv = "";
    try {
      const dir = path.dirname(sockPath);
      fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
      fs.chmodSync(dir, 0o700);
      sweepDead(dir);
      priv = path.join(dir, privateName());
      const srv = net.createServer(onConnection);
      srv.maxConnections = MAX_CONNECTIONS;
      await new Promise<void>((resolve, reject) => {
        srv.once("error", reject);
        // The umask is process-wide; hold it only across the bind, which is
        // synchronous, so the socket is created private without a chmod window.
        const prev = process.umask(0o077);
        try {
          srv.listen(priv, resolve);
        } catch (e) {
          reject(e);
        } finally {
          process.umask(prev);
        }
      });
      srv.on("error", () => {});
      const st = fs.statSync(priv, { bigint: true });
      try {
        fs.renameSync(priv, sockPath);
      } catch (e) {
        srv.close();
        throw e;
      }
      priv = "";
      owned = { dev: st.dev, ino: st.ino };
      server = srv;
    } catch (e) {
      if (priv) fs.rmSync(priv, { force: true });
      warn("could not serve " + sockPath + ": " + String(e) +
        "; until this pi restarts it gets no socket wakes and none of the ledger's records (agent-deck msg read shows them)");
    }
  };

  // serving reports whether the socket at the public path is the one this
  // runtime bound: not before start, not after stop, and not once a later
  // starter has renamed its own over it.
  const serving = (): boolean => {
    if (!server || !owned) return false;
    try {
      const st = fs.statSync(sockPath, { bigint: true });
      return st.dev === owned.dev && st.ino === owned.ino;
    } catch {
      return false;
    }
  };

  const stop = async () => {
    const srv = server;
    if (!srv) return;
    server = null;
    const mine = owned;
    owned = null;
    if (pending) clearTimeout(pending.timer);
    pending = null;
    try {
      await new Promise<void>((resolve) => {
        srv.close(() => resolve());
        for (const c of conns) c.destroy();
      });
      // The public path is ours only while it still holds the socket we bound;
      // gone before shutdown returns, so a /reload runtime can bind at once.
      const st = fs.statSync(sockPath, { bigint: true });
      if (mine && st.dev === mine.dev && st.ino === mine.ino) fs.rmSync(sockPath, { force: true });
    } catch {
      // Shutdown must never throw into pi, and a path that is gone is fine.
    }
  };

  // The run a doorbell asked for has started: answer it now, before the caller
  // awaits hook-handler.
  const rang = (event?: any) => {
    const nonce = event?.messageType === "custom" ? event.message?.details?.nonce : undefined;
    if (pending && pending.nonce === nonce) settle(pending, "submitted");
  };

  return { start, stop, rang, serving };
}

export default function (pi: ExtensionAPI) {
  if (!INSTANCE_ID) return;
  // A guest of the pane inherited another pi's pid (see the header). The host
  // writes its own, for everything it starts to inherit.
  const host = (process.env[HOST_PID_ENV] ?? "").trim();
  const guest = host !== "" && host !== String(process.pid);
  if (!guest) process.env[HOST_PID_ENV] = String(process.pid);
  const wake = SOCKET && !guest ? serveWakeSocket(pi, SOCKET) : undefined;

  // The run's state. pi fires input before agent_start, so only the turn is
  // reset on agent_start; the input is consumed when the run settles.
  let lastInput = "";
  let lastTurn: { entryId: string; text: string; outcome: string; stopReason: string } | undefined;
  // The run token, minted at agent_start; carried with the settles capability bit.
  let runId = "";
  const runFields = () => ({ settles: true, run_id: runId || undefined });
  // The delivery token of the context just injected, echoed on the next turn_start.
  let injectedToken: string | undefined;
  // The last token injected, and the last one already reported on a run start:
  // the next before_agent_start carries it once (prior_injected_token), so the
  // receipt reaches the ledger even if the daemon has not ingested the echo.
  let lastInjected: string | undefined;
  let lastReported: string | undefined;
  // Every payload of a guest of the pane says so, and so does every payload of
  // the pi serving the socket (see the header).
  const mark = (fields: Record<string, unknown> = {}) =>
    guest ? { ...fields, pane_guest: true } : wake?.serving() ? { ...fields, pane_host: true } : fields;

  // Returning emit's promise (rather than firing and forgetting) is what makes
  // pi await delivery before it runs the next handler.
  pi.on("session_start", async (event?: any, ctx?: any) => {
    await wake?.start(event, ctx);
    await emit("session_start", mark());
  });
  pi.on("agent_start", () => {
    runId = crypto.randomUUID();
    lastTurn = undefined;
  });
  pi.on("input", (event?: any) => {
    if (typeof event?.text === "string") lastInput = event.text;
  });
  pi.on("turn_start", () => {
    const token = injectedToken;
    injectedToken = undefined;
    return emit("turn_start", mark({ ...runFields(), injected_token: token }));
  });
  pi.on("turn_end", (event?: any) => {
    lastTurn = {
      entryId: typeof event?.messageEntryId === "string" ? event.messageEntryId : "",
      text: assistantText(event?.message),
      outcome: String(event?.outcome ?? ""),
      stopReason: String(event?.message?.stopReason ?? ""),
    };
    return emit("turn_end", mark(runFields()));
  });
  pi.on("agent_settled", (event?: any, ctx?: any) => {
    const turn = lastTurn;
    const prompt = lastInput;
    lastTurn = undefined;
    lastInput = "";
    let sessionId: string | undefined;
    try {
      sessionId = ctx?.sessionManager?.getSessionId();
    } catch {}
    // Escape during tool execution still ends the turn as completed. A last
    // turn that stopped to run tools is not an answer: stopReason shows it,
    // and aborted shows the abort itself.
    const answered = turn?.outcome === "completed" && turn.stopReason !== "toolUse" && event?.aborted !== true;
    const reported = answered ? turn.text.trim() : "";
    return emit("agent_settled", mark({
      ...runFields(),
      session_id: sessionId,
      turn_id: turn?.entryId || undefined,
      last_assistant_message: reported ? tailBytes(reported, MAX_TEXT_BYTES) : undefined,
      prompt: prompt || undefined,
    }));
  });
  pi.on("session_shutdown", async () => {
    await wake?.stop();
    await emit("session_shutdown", mark());
  });
  // A doorbell is answered before hook-handler is awaited. A human prompt
  // carries no nonce.
  pi.on("before_agent_start", async (event?: any, ctx?: any) => {
    wake?.rang(event);
    const isWake = event?.messageType === "custom" && event.message?.customType === WAKE_TYPE;
    const prompt = isWake ? event.message.content : event?.messageType === "user" ? event.prompt : undefined;
    if (typeof prompt !== "string") return;
    let sessionId: string | undefined;
    try {
      sessionId = ctx?.sessionManager?.getSessionId();
    } catch {}
    const nonce = isWake ? event.message.details?.nonce : undefined;
    const prior = lastInjected !== lastReported ? lastInjected : undefined;
    lastReported = lastInjected;
    const context = await run("before_agent_start", mark({
      session_id: sessionId,
      prompt,
      wake_nonce: typeof nonce === "string" ? nonce : undefined,
      prior_injected_token: prior,
    }), INJECT_TIMEOUT_MS);
    if (context === null) {
      console.error("agent-deck: injection dropped: hook-handler failed or timed out; its records stay pending for the next run");
      return;
    }
    // The hook's last line may be the delivery token: strip it and remember it,
    // to echo once pi really has the context (its stdout alone proves nothing).
    const m = /\n?<!-- agent-deck-delivery:([0-9A-Za-z]+) -->\s*$/.exec(context);
    const text = (m ? context.slice(0, m.index) : context).trim();
    if (!text) return;
    if (m) {
      injectedToken = m[1];
      lastInjected = m[1];
    }
    return { message: { customType: CONTEXT_TYPE, content: text, display: false } };
  });
}
