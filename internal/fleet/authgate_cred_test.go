package fleet

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// SubstateAuthGate's #1816 credential grouping, and the default-unchanged
// guarantee that the maintainer ruling depends on.

// ungroupedHaltMessage is the EXACT halt string this gate produced before
// #1816. It is duplicated here on purpose: the guarantee under test is that the
// default path's bytes did not move, and a test that built the expectation from
// the same code it is checking could not detect that they had.
const ungroupedHaltMessage = `auth circuit open: 2 session(s) booted into an auth failure (substate "auth-401") — recovering the rest would keep re-forking the shared credential; re-authenticate, then re-run`

// credGate returns a gate whose credential identity is a fixed per-title lookup.
func credGate(halt int, group bool, dirs map[string]string) *SubstateAuthGate {
	return &SubstateAuthGate{
		HaltAfter:         halt,
		GroupByCredential: group,
		Grouper: &AuthCredentialGrouper{
			ConfigDir: func(inst *session.Instance) string {
				if inst == nil {
					return ""
				}
				return dirs[inst.Title]
			},
		},
	}
}

func auth401Report() VerifyReport {
	return VerifyReport{PaneAlive: true, ToolStarted: true, Substate: string(session.SubstateAuth401)}
}

func healthyReport() VerifyReport {
	return VerifyReport{PaneAlive: true, ToolStarted: true, Status: string(session.StatusRunning)}
}

// DEFAULT UNCHANGED: with the flag off the halt message is byte-identical to the
// pre-#1816 string, and nothing is collected.
func TestFlagOffLeavesHaltMessageByteIdentical(t *testing.T) {
	g := credGate(2, false, map[string]string{
		"alpha": "/home/u/.claude-work",
		"bravo": "/home/u/.claude-work",
	})

	g.Observe(heldInstance("alpha", "work"), auth401Report(), nil)
	g.Observe(heldInstance("bravo", "work"), auth401Report(), nil)

	ok, reason := g.Allow()
	if ok {
		t.Fatal("circuit should be open after 2 auth failures")
	}
	if reason != ungroupedHaltMessage {
		t.Errorf("default halt message changed.\n got: %q\nwant: %q", reason, ungroupedHaltMessage)
	}
	if sum := g.AuthFailuresByCredential(); sum.Held != 0 || len(sum.Groups) != 0 {
		t.Errorf("grouping collected %+v with the flag off; opt-in means opt-in", sum)
	}
	if g.AuthFailures() != 2 {
		t.Errorf("AuthFailures = %d, want 2 — the existing counter must be untouched", g.AuthFailures())
	}
}

// OPT-IN: with the flag on, one dead credential behind N boots is ONE
// escalation naming that credential.
func TestFlagOnHaltNamesTheDeadCredentialOnce(t *testing.T) {
	g := credGate(2, true, map[string]string{
		"alpha": "/home/u/.claude-work",
		"bravo": "/home/u/.claude-work",
	})

	g.Observe(heldInstance("alpha", "work"), auth401Report(), nil)
	g.Observe(heldInstance("bravo", "work"), auth401Report(), nil)

	ok, reason := g.Allow()
	if ok {
		t.Fatal("circuit should be open")
	}
	if !strings.Contains(reason, "1 credential(s) dead behind 2 booted session(s)") {
		t.Errorf("halt message does not aggregate by credential: %s", reason)
	}
	if !strings.Contains(reason, "work") {
		t.Errorf("halt message does not name the credential: %s", reason)
	}
	if got := strings.Count(reason, "auth-401: "); got != 1 {
		t.Errorf("got %d per-credential escalations, want exactly 1:\n%s", got, reason)
	}

	sum := g.AuthFailuresByCredential()
	if sum.Credentials != 1 || sum.Held != 2 {
		t.Errorf("summary = %+v, want 1 credential holding 2 sessions", sum)
	}
}

// Two credentials failing in the same sweep stay two escalations.
func TestTwoCredentialsInOneSweepStayTwoEscalations(t *testing.T) {
	g := credGate(2, true, map[string]string{
		"alpha": "/home/u/.claude-work",
		"bravo": "/home/u/.claude-personal",
	})

	g.Observe(heldInstance("alpha", "work"), auth401Report(), nil)
	g.Observe(heldInstance("bravo", "personal"), auth401Report(), nil)

	_, reason := g.Allow()
	if !strings.Contains(reason, "2 credential(s) dead") {
		t.Errorf("halt message should report 2 dead credentials: %s", reason)
	}
	if got := strings.Count(reason, "auth-401: "); got != 2 {
		t.Errorf("got %d escalations, want 2:\n%s", got, reason)
	}
}

// A CREDENTIAL THAT RECOVERS MID-SWEEP must stop being escalated as dead. The
// operator acting on a stale "re-authenticate account work" would be chasing a
// credential that already works.
func TestCredentialRecoveringMidSweepIsNotEscalatedAsDead(t *testing.T) {
	g := credGate(2, true, map[string]string{
		"alpha":   "/home/u/.claude-work",
		"bravo":   "/home/u/.claude-work",
		"charlie": "/home/u/.claude-personal",
	})

	g.Observe(heldInstance("alpha", "work"), auth401Report(), nil)
	// The token is repaired: a later boot on the SAME credential authenticates.
	g.Observe(heldInstance("bravo", "work"), healthyReport(), nil)
	g.Observe(heldInstance("charlie", "personal"), auth401Report(), nil)

	sum := g.AuthFailuresByCredential()
	if sum.Credentials != 1 {
		t.Fatalf("Credentials = %d, want 1 — the work credential recovered", sum.Credentials)
	}
	if sum.Recovered != 1 {
		t.Errorf("Recovered = %d, want 1", sum.Recovered)
	}

	_, reason := g.Allow()
	if !strings.Contains(reason, "1 credential(s) dead") {
		t.Errorf("halt should count only the still-dead credential: %s", reason)
	}
	// The recovered credential is still reported, but as retryable — never as
	// something to re-authenticate.
	if !strings.Contains(reason, "it is NOT dead; retry those sessions") {
		t.Errorf("recovered credential should be reported as retryable: %s", reason)
	}
}

// Last observation wins: a credential that authenticates and THEN 401s is dead
// now, whatever it did earlier.
func TestCredentialThatFailsAfterRecoveringIsDeadAgain(t *testing.T) {
	g := credGate(2, true, map[string]string{
		"alpha": "/home/u/.claude-work",
		"bravo": "/home/u/.claude-work",
	})

	g.Observe(heldInstance("alpha", "work"), healthyReport(), nil)
	g.Observe(heldInstance("alpha", "work"), auth401Report(), nil)
	g.Observe(heldInstance("bravo", "work"), auth401Report(), nil)

	sum := g.AuthFailuresByCredential()
	if sum.Credentials != 1 || sum.Recovered != 0 {
		t.Fatalf("summary = %+v, want the credential counted dead again", sum)
	}
}

// THE FLAG TOGGLED WHILE BOOTS ARE ALREADY OBSERVED: the grouped lines then
// account for fewer sessions than the headline. The message has to say so —
// listing 1 session under a headline of 3 would read as "the other 2 are fine".
func TestGroupingEnabledMidSweepDeclaresTheUnattributedBoots(t *testing.T) {
	g := credGate(3, false, map[string]string{
		"alpha":   "/home/u/.claude-work",
		"bravo":   "/home/u/.claude-work",
		"charlie": "/home/u/.claude-work",
	})

	g.Observe(heldInstance("alpha", "work"), auth401Report(), nil)
	g.Observe(heldInstance("bravo", "work"), auth401Report(), nil)
	// Operator flips the opt-in on partway through.
	g.GroupByCredential = true
	g.Observe(heldInstance("charlie", "work"), auth401Report(), nil)

	ok, reason := g.Allow()
	if ok {
		t.Fatal("circuit should be open after 3 auth failures")
	}
	if !strings.Contains(reason, "behind 3 booted session(s)") {
		t.Errorf("headline lost the boots observed before the flag: %s", reason)
	}
	if !strings.Contains(reason, "2 earlier boot(s) were observed before credential grouping was enabled") {
		t.Errorf("message must declare the boots it could not attribute: %s", reason)
	}
	if !strings.Contains(reason, "1 session(s) held") {
		t.Errorf("expected the one attributed session to be listed: %s", reason)
	}
}

// Non-auth boots never open the circuit, and never invent a credential group.
func TestHealthyBootsAloneNeverOpenTheCircuit(t *testing.T) {
	g := credGate(2, true, map[string]string{"alpha": "/home/u/.claude-work"})

	for i := 0; i < 5; i++ {
		g.Observe(heldInstance("alpha", "work"), healthyReport(), nil)
	}

	if ok, _ := g.Allow(); !ok {
		t.Fatal("healthy boots must not open the auth circuit")
	}
	if sum := g.AuthFailuresByCredential(); len(sum.Groups) != 0 {
		t.Errorf("healthy boots created groups: %+v", sum.Groups)
	}
}

// An unattributable boot in the sweep gets the unknown bucket rather than being
// counted as one of the dead credentials.
func TestUnattributableBootDoesNotCountAsADeadCredential(t *testing.T) {
	g := credGate(2, true, map[string]string{
		"alpha":  "/home/u/.claude-work",
		"orphan": "",
	})

	g.Observe(heldInstance("alpha", "work"), auth401Report(), nil)
	g.Observe(heldInstance("orphan", ""), auth401Report(), nil)

	sum := g.AuthFailuresByCredential()
	if sum.Credentials != 1 {
		t.Errorf("Credentials = %d, want 1 — unknown is not a credential", sum.Credentials)
	}
	if sum.Unattributed != 1 {
		t.Errorf("Unattributed = %d, want 1", sum.Unattributed)
	}

	_, reason := g.Allow()
	if !strings.Contains(reason, "NOT known to share one credential") {
		t.Errorf("the unknown bucket must be reported honestly: %s", reason)
	}
}

// With the flag on but nothing attributable at all, the message falls back to
// the ungrouped wording rather than announcing "0 credentials" — which would
// read as "no credential problem" at the exact moment the circuit opened.
func TestGroupedMessageFallsBackWhenNothingWasCollected(t *testing.T) {
	g := credGate(2, true, nil)
	// Substate that is not auth-401: seen never increments via grouping.
	g.Observe(heldInstance("alpha", "work"), VerifyReport{Substate: string(session.SubstateAuth401)}, nil)
	g.Observe(heldInstance("bravo", "work"), VerifyReport{Substate: string(session.SubstateAuth401)}, nil)

	_, reason := g.Allow()
	// Both boots WERE grouped (into unknown), so this must be the grouped form.
	if !strings.Contains(reason, "auth-401: 2 session(s) held on an unattributable credential") {
		t.Errorf("expected the unknown bucket to be reported: %s", reason)
	}

	// A gate that collected nothing at all falls back.
	empty := credGate(1, true, nil)
	empty.seen = 5
	_, emptyReason := empty.Allow()
	if !strings.Contains(emptyReason, "5 session(s) booted into an auth failure") {
		t.Errorf("empty grouped gate should fall back to the ungrouped wording: %s", emptyReason)
	}
}

// ---------------------------------------------------------------------------
// The shape the P2a/P2b/P2c tests still did not build: the host dimension
// crossed with the SWEEP path, and with the recovered-mid-sweep map.
//
// P2a was reported and fixed against the query (AuthCredentialGrouper). The gate
// resolves identity through the same Identify, so it inherits the fix — but the
// gate is the higher-stakes surface (it is what HALTS a fleet recovery), and
// "inherits it" was not proven anywhere. The crossing below is the one that
// would actually hurt: g.recovered is keyed by credential key, so if local and
// remote stores ever shared a key again, a LOCAL boot authenticating would
// silently mark a REMOTE dead credential as recovered and drop it out of the
// halt message. That is the original defect re-entering through a second door.
// ---------------------------------------------------------------------------

func sshGateInstance(title, account, sshHost string) *session.Instance {
	inst := heldInstance(title, account)
	inst.SSHHost = sshHost
	return inst
}

// The sweep's halt message must separate a local store from a remote one on the
// same path, and must not tell the operator a local re-auth covers both.
func TestSweepHaltSeparatesLocalAndRemoteStores(t *testing.T) {
	g := credGate(2, true, map[string]string{
		"local-one":  "/home/u/.claude-work",
		"remote-one": "/home/u/.claude-work",
	})

	g.Observe(heldInstance("local-one", "work"), auth401Report(), nil)
	g.Observe(sshGateInstance("remote-one", "work", "box-b"), auth401Report(), nil)

	sum := g.AuthFailuresByCredential()
	if sum.Credentials != 2 {
		t.Fatalf("Credentials = %d, want 2 — the sweep merged a local and a remote store", sum.Credentials)
	}

	_, reason := g.Allow()
	if !strings.Contains(reason, "2 credential(s) dead") {
		t.Errorf("halt should report 2 dead credentials: %s", reason)
	}
	if !strings.Contains(reason, "box-b") {
		t.Errorf("halt must name the remote host: %s", reason)
	}
	if !strings.Contains(reason, "re-authenticating locally will NOT reach it") {
		t.Errorf("halt must not imply a local re-auth fixes the remote store: %s", reason)
	}
}

// THE CROSSING: a local credential recovering mid-sweep must NOT clear a remote
// credential that happens to share the same config path. If it did, the fleet
// recovery would resume against a still-dead remote token.
func TestLocalRecoveryDoesNotClearRemoteDeadCredential(t *testing.T) {
	g := credGate(2, true, map[string]string{
		"local-one":  "/home/u/.claude-work",
		"local-two":  "/home/u/.claude-work",
		"remote-one": "/home/u/.claude-work",
	})

	// Both stores fail...
	g.Observe(heldInstance("local-one", "work"), auth401Report(), nil)
	g.Observe(sshGateInstance("remote-one", "work", "box-b"), auth401Report(), nil)
	// ...then the LOCAL one is repaired.
	g.Observe(heldInstance("local-two", "work"), healthyReport(), nil)

	sum := g.AuthFailuresByCredential()

	if sum.Recovered != 1 {
		t.Errorf("Recovered = %d, want 1 (the local store only)", sum.Recovered)
	}
	if sum.Credentials != 1 {
		t.Fatalf("Credentials = %d, want 1 — the remote store is still dead", sum.Credentials)
	}
	// The surviving dead credential must be the REMOTE one.
	var stillDead *CredentialGroup
	for i := range sum.Groups {
		if !sum.Groups[i].Recovered {
			stillDead = &sum.Groups[i]
		}
	}
	if stillDead == nil {
		t.Fatal("a local recovery cleared every credential, including the remote one")
	}
	if !stillDead.Credential.IsRemote() || stillDead.Credential.Host != "box-b" {
		t.Errorf("still-dead credential = %+v, want the box-b remote store", stillDead.Credential)
	}
}

// And the mirror: a remote recovery must not clear the local store.
func TestRemoteRecoveryDoesNotClearLocalDeadCredential(t *testing.T) {
	g := credGate(2, true, map[string]string{
		"local-one":  "/home/u/.claude-work",
		"remote-one": "/home/u/.claude-work",
		"remote-two": "/home/u/.claude-work",
	})

	g.Observe(heldInstance("local-one", "work"), auth401Report(), nil)
	g.Observe(sshGateInstance("remote-one", "work", "box-b"), auth401Report(), nil)
	g.Observe(sshGateInstance("remote-two", "work", "box-b"), healthyReport(), nil)

	sum := g.AuthFailuresByCredential()
	if sum.Recovered != 1 || sum.Credentials != 1 {
		t.Fatalf("summary = %+v, want 1 recovered (remote) and 1 still dead (local)", sum)
	}
	for _, grp := range sum.Groups {
		if !grp.Recovered && grp.Credential.IsRemote() {
			t.Error("the remote store recovered but is still reported dead")
		}
		if grp.Recovered && !grp.Credential.IsRemote() {
			t.Error("a remote recovery cleared the local store")
		}
	}
}

// An aliased Claude-compatible tool must aggregate in the SWEEP too, not only in
// the query — otherwise the halt message under-counts a dead credential's blast
// radius by silently dropping the aliased sessions.
func TestSweepAggregatesClaudeCompatibleAlias(t *testing.T) {
	cfg, err := session.LoadUserConfig()
	if err != nil || cfg == nil {
		t.Skipf("LoadUserConfig unavailable: %v", err)
	}
	if cfg.Tools == nil {
		cfg.Tools = map[string]session.ToolDef{}
	}
	cfg.Tools["claude_wrapper"] = session.ToolDef{Command: "claude-wrapper", CompatibleWith: "claude"}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	g := credGate(2, true, map[string]string{
		"plain":   "/home/u/.claude-work",
		"aliased": "/home/u/.claude-work",
	})

	aliased := heldInstance("aliased", "work")
	aliased.Tool = "claude_wrapper"

	g.Observe(heldInstance("plain", "work"), auth401Report(), nil)
	g.Observe(aliased, auth401Report(), nil)

	sum := g.AuthFailuresByCredential()
	if sum.Unattributed != 0 {
		t.Fatalf("Unattributed = %d, want 0 — the sweep dropped the aliased session", sum.Unattributed)
	}
	if sum.Credentials != 1 || len(sum.Groups[0].Sessions) != 2 {
		t.Fatalf("summary = %+v, want one credential holding both sessions", sum)
	}
}
