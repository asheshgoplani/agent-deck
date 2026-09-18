// AGENTDECK PI HOOK EXTENSION v1
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
// environment nothing is spawned and no handler is registered. Every emit is
// fire-and-forget — pi never waits on it and a missing or failing agent-deck
// binary can never block or crash a turn.

import { spawn } from "node:child_process";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const INSTANCE_ID = (process.env.AGENTDECK_INSTANCE_ID ?? "").trim();

function emit(event: string): void {
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
    // must never take the session down with it.
    child.on("error", () => {});
    child.stdin.on("error", () => {});
    child.stdin.end(payload);
  } catch {
    // ignored on purpose (see above)
  }
}

export default function (pi: ExtensionAPI) {
  if (!INSTANCE_ID) return;

  pi.on("session_start", async () => emit("session_start"));
  pi.on("turn_start", async () => emit("turn_start"));
  pi.on("turn_end", async () => emit("turn_end"));
  pi.on("session_shutdown", async () => emit("session_shutdown"));
}
