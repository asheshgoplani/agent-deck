// AGENTDECK PI HOOK EXTENSION v3
//
// Managed by `agent-deck pi-hooks install`. Local edits are overwritten on the
// next install/upgrade, and `agent-deck pi-hooks uninstall` deletes this file.
//
// It forwards pi's lifecycle events to `agent-deck hook-handler` on stdin so a
// pi session's light is driven by real events instead of pane-regex guesses.
// Event mapping (agent-deck side, cmd/agent-deck/hook_handler.go):
//
//   session_start    -> waiting   (at the prompt)
//   turn_start       -> running   (the agent is working)
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
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const INSTANCE_ID = (process.env.AGENTDECK_INSTANCE_ID ?? "").trim();
const EMIT_TIMEOUT_MS = 2000;
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

function emit(event: string, fields: Record<string, unknown> = {}): Promise<void> {
  return new Promise((resolve) => {
    try {
      const payload = JSON.stringify({
        hook_event_name: event,
        cwd: process.cwd(),
        source: "pi",
        ...fields,
      });
      const child = spawn("agent-deck", ["hook-handler"], {
        stdio: ["pipe", "ignore", "ignore"],
      });
      // A missing binary surfaces as an "error" event, and a handler that
      // exited early surfaces as EPIPE on stdin. Swallow both: status reporting
      // must never take the session down with it. The timeout guarantees emit
      // always settles even if the child wedges without exiting.
      child.on("error", () => resolve());
      child.on("close", () => resolve());
      setTimeout(resolve, EMIT_TIMEOUT_MS).unref();
      child.stdin.on("error", () => {});
      child.stdin.end(payload);
    } catch {
      resolve();
    }
  });
}

export default function (pi: ExtensionAPI) {
  if (!INSTANCE_ID) return;

  // The run's state. pi fires input before agent_start, so only the turn is
  // reset on agent_start; the input is consumed when the run settles.
  let lastInput = "";
  let lastTurn: { entryId: string; text: string; outcome: string; stopReason: string } | undefined;

  // Returning emit's promise (rather than firing and forgetting) is what makes
  // pi await delivery before it runs the next handler.
  pi.on("session_start", () => emit("session_start"));
  pi.on("agent_start", () => {
    lastTurn = undefined;
  });
  pi.on("input", (event?: any) => {
    if (typeof event?.text === "string") lastInput = event.text;
  });
  pi.on("turn_start", () => emit("turn_start"));
  pi.on("turn_end", (event?: any) => {
    lastTurn = {
      entryId: typeof event?.messageEntryId === "string" ? event.messageEntryId : "",
      text: assistantText(event?.message),
      outcome: String(event?.outcome ?? ""),
      stopReason: String(event?.message?.stopReason ?? ""),
    };
    return emit("turn_end");
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
    return emit("agent_settled", {
      session_id: sessionId,
      turn_id: turn?.entryId || undefined,
      last_assistant_message: reported ? tailBytes(reported, MAX_TEXT_BYTES) : undefined,
      prompt: prompt || undefined,
    });
  });
  pi.on("session_shutdown", () => emit("session_shutdown"));
}
