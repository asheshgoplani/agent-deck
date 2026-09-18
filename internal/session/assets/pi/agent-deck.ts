// AGENTDECK PI HOOK EXTENSION v2
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

function emit(event: string): Promise<void> {
  return new Promise((resolve) => {
    try {
      const payload = JSON.stringify({
        hook_event_name: event,
        cwd: process.cwd(),
        source: "pi",
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

  // Returning emit's promise (rather than firing and forgetting) is what makes
  // pi await delivery before it runs the next handler.
  pi.on("session_start", () => emit("session_start"));
  pi.on("turn_start", () => emit("turn_start"));
  pi.on("turn_end", () => emit("turn_end"));
  pi.on("session_shutdown", () => emit("session_shutdown"));
}
