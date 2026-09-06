import { test, describe } from "repterm";

// Tests remote session creation via the SSH runner (the core logic that n/N keys invoke).
// This validates that CreateSession correctly runs "add --quick --json" + "session start"
// on the remote, and that the session is created, started, and attachable.

const remoteHost = process.env.AGENTDECK_E2E_REMOTE_HOST;
const remoteAgentDeckPath = process.env.AGENTDECK_E2E_REMOTE_AGENT_DECK_PATH || "agent-deck";
const sshBin = process.env.AGENTDECK_E2E_SSH_BIN || "/usr/bin/ssh";

function remoteCmd(agentDeckArgs: string): string {
  return `${sshBin} ${remoteHost} "${remoteAgentDeckPath} ${agentDeckArgs}"`;
}

function ensureConfigured(): boolean {
  if (remoteHost) return true;
  console.log("Skipping remote e2e test: set AGENTDECK_E2E_REMOTE_HOST to enable");
  return false;
}

describe("Remote Session Creation", () => {
  test("add + start creates a running session on remote", async ({ terminal }) => {
    if (!ensureConfigured()) return;

    // Count sessions before
    const before = await terminal.run(
      remoteCmd(`list --json 2>/dev/null || echo '[]'`),
      { silent: true }
    );
    let initialCount = 0;
    try {
      const s = JSON.parse(before.stdout);
      initialCount = Array.isArray(s) ? s.length : 0;
    } catch {}

    // Step 1: Create session (same as CreateSession step 1)
    const create = await terminal.run(
      remoteCmd("add --quick --json"),
    );
    if (create.code !== 0) {
      throw new Error(`add failed (exit ${create.code}): ${create.stderr || create.stdout}`);
    }

    let sessionId = "";
    let sessionTitle = "";
    try {
      const result = JSON.parse(create.stdout);
      sessionId = result.id;
      sessionTitle = result.title;
    } catch (e) {
      throw new Error(`Failed to parse add output: ${create.stdout}`);
    }
    if (!sessionId) throw new Error("add returned empty session ID");

    // Step 2: Start session (same as CreateSession step 2)
    const start = await terminal.run(
      remoteCmd(`session start '${sessionTitle}'`),
      { timeout: 20000 }
    );
    if (start.code !== 0) {
      throw new Error(`session start failed (exit ${start.code}): ${start.stderr || start.stdout}`);
    }

    // Verify session exists and is started
    const after = await terminal.run(
      remoteCmd("list --json"),
      { silent: true }
    );
    let finalCount = 0;
    let newSession: any = null;
    try {
      const s = JSON.parse(after.stdout);
      if (Array.isArray(s)) {
        finalCount = s.length;
        newSession = s.find((x: any) => x.id === sessionId);
      }
    } catch {}

    if (finalCount <= initialCount) {
      throw new Error(`Expected more sessions. Before: ${initialCount}, After: ${finalCount}`);
    }
    if (!newSession) {
      throw new Error(`Session ${sessionId} not found in list`);
    }

    // Session should not be in error state after start
    if (newSession.status === "error") {
      throw new Error(`Session ${sessionId} is in error state after start`);
    }

    console.log(`  Created+started: ${newSession.title} (${sessionId}) status=${newSession.status}`);
  });

  // The TUI's remote new-session dialog forwards its fields as explicit
  // `add` flags (title, tool, model, account, worktree, sandbox, ...). Exercise
  // the non-default shape too, so a remote whose `add` rejects one of the
  // forwarded flags shows up here rather than as a silent TUI failure.
  test("add with explicit title, tool and model creates the configured session", async ({ terminal }) => {
    if (!ensureConfigured()) return;

    const title = `e2e-remote-options-${Date.now()}`;
    const create = await terminal.run(
      remoteCmd(`add --json -t '${title}' -c claude --model sonnet`),
    );
    if (create.code !== 0) {
      throw new Error(`add with options failed (exit ${create.code}): ${create.stderr || create.stdout}`);
    }
    let result: any;
    try {
      result = JSON.parse(create.stdout);
    } catch {
      throw new Error(`Failed to parse add output: ${create.stdout}`);
    }
    if (!result.id) throw new Error("add returned empty session ID");
    if (result.title !== title) {
      throw new Error(`Expected title ${title}, got ${result.title}`);
    }

    const show = await terminal.run(remoteCmd(`session show ${result.id} --json`), { silent: true });
    const shown = JSON.parse(show.stdout);
    if (shown.tool !== "claude") {
      throw new Error(`Expected tool claude, got ${shown.tool}`);
    }
    console.log(`  Created with options: ${shown.title} (${result.id}) tool=${shown.tool}`);
  });

  test("session is attachable after create+start", async ({ terminal }) => {
    if (!ensureConfigured()) return;

    // Get the latest session
    const list = await terminal.run(
      remoteCmd("list --json"),
      { silent: true }
    );
    const sessions = JSON.parse(list.stdout);
    const latest = sessions[sessions.length - 1];

    // Verify attach doesn't fail with "not found"
    const attach = await terminal.run(
      `${sshBin} -t ${remoteHost} "${remoteAgentDeckPath} session attach ${latest.id}" < /dev/null`,
      { timeout: 5000 }
    );
    if (attach.stderr?.includes("not found") || attach.stdout?.includes("not found")) {
      throw new Error(`Session ${latest.id} not found for attach`);
    }

    console.log(`  Attach check for ${latest.title} (${latest.id}): OK`);
  });
});
