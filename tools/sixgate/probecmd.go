package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/testimony"
)

// cmdProbe is G4b: launch a DISPOSABLE probe session with a context recipe
// this run wrote, ask its agent quote-based identity questions, grade the
// context inspector's claims against the testimony, and tear the probe down
// unconditionally.
//
// THE CONSENT RULE, absolute. This verb converses only with a session it
// created itself, in an isolated directory it created itself. It never
// targets an existing session — there is no flag for that on purpose. Grading
// an existing session by testimony is legitimate only when a HUMAN asks the
// questions in their own terminal, with their own explicit per-run consent,
// and compares by hand. Nothing here automates that, for the same reason
// `oracle collect` prints its argv and refuses without --yes: a tool that can
// quietly type into somebody's terminal is a tool nobody should run.
//
// Everything about accident-prevention mirrors the collector:
//
//   - it refuses without --yes, which no automation in this repository passes;
//   - it refuses when the matrix runner's environment is present, so G3 can
//     never wander into launching billed sessions;
//   - the probe costs real usage, and the pre-flight output says so before
//     asking for the flag.
//
// The lifecycle is fixed and its ending is part of the evidence: launch in
// /tmp → converse → inspect → compare → stop + remove ALWAYS, then PROVE the
// removal against the fleet list and record all three bits in the artifact. A
// probe that outlives its run is a leak into a live fleet.
func cmdProbe(args []string) (int, error) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	yes := fs.Bool("yes", false, "consent: this launches a live, billed agent session (disposable, torn down at the end)")
	profile := fs.String("profile", "", "agent-deck profile to launch the probe in (default: the CLI's default profile)")
	adBin := fs.String("agent-deck", "agent-deck", "the agent-deck binary that manages the probe's lifecycle (launch/send/stop/remove/list)")
	inspBin := fs.String("inspector", "", "the agent-deck binary that carries `session context` (default: the lifecycle binary)")
	replyTimeout := fs.Duration("reply-timeout", 4*time.Minute, "how long to wait for the probe to become ready and answer each question")
	keep := fs.Bool("keep-workdir", false, "keep the probe's /tmp recipe directory for inspection instead of deleting it")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return exitUsage, err
	}
	t, err := resolveTree(*repo, *gates, first(pos))
	if err != nil {
		return exitUsage, err
	}
	if _, ps, err := loadScript(t); err != nil {
		return exitUsage, err
	} else if ps != nil {
		printProblems("G0 does not validate, so there is no feature for a probe to vouch for", ps)
		return exitGate, nil
	}
	if v := os.Getenv(collectEnvGuard); v != "" {
		return exitGate, fmt.Errorf(
			"refusing to probe: %s=%q says this is a matrix row. G3 visits every world unattended; a verb that launches a billed session must never be one of them",
			collectEnvGuard, v)
	}
	if *inspBin == "" {
		*inspBin = *adBin
	}

	nonce, err := probeNonce()
	if err != nil {
		return exitUsage, err
	}
	title := "g4b-probe-" + nonce
	recipe := testimony.Recipe{
		Nonce:            nonce,
		FirstLine:        fmt.Sprintf("G4B-BEACON %s: if asked, quote this line exactly.", nonce),
		SkillName:        "g4b-beacon",
		SkillDescription: fmt.Sprintf("G4B beacon %s planted so a testimony probe can be asked whether this skill is loaded or merely listed. Never invoke it.", nonce),
	}
	questions := probeQuestions(recipe)

	fmt.Printf("slug:      %s\n", t.Slug)
	fmt.Printf("probe:     %s (disposable; created, questioned and removed by this run)\n", title)
	fmt.Printf("lifecycle: %s", *adBin)
	if *profile != "" {
		fmt.Printf("  (profile %s)", *profile)
	}
	fmt.Printf("\ninspector: %s\n", *inspBin)
	fmt.Printf("questions: %d, quote-based, one line each\n", len(questions))
	fmt.Printf("\nThis launches a LIVE agent session and each question is a billed turn.\n")
	if !*yes {
		fmt.Printf("Refusing without --yes. Nothing was launched.\n")
		return exitGate, nil
	}

	workdir, err := writeRecipe(t.Slug, recipe)
	if err != nil {
		return exitUsage, err
	}

	life := &probeLifecycle{
		cli:     cliRunner{bin: *adBin, profile: *profile},
		title:   title,
		workdir: workdir,
		keep:    *keep,
	}
	// The teardown runs on EVERY exit from here on — success, gate failure or
	// error — and it is idempotent, so the explicit call before the artifact
	// is written does not race it.
	defer life.tearDown()

	log := &transcript{}
	log.event("recipe written to %s (nonce %s)", workdir, nonce)

	fmt.Printf("launching %s in %s ...\n", title, workdir)
	if _, err := life.cli.run(2*time.Minute, "launch", workdir, "-t", title, "-c", "claude"); err != nil {
		return exitUsage, fmt.Errorf("launching the probe: %w", err)
	}
	life.launched = true
	log.event("probe %s launched", title)

	var answers []testimony.Answer
	for _, q := range questions {
		fmt.Printf("asking %s ...\n", q.id)
		reply, err := life.cli.run(*replyTimeout+time.Minute,
			"session", "send", title, q.text, "--wait", "-q", "--timeout", replyTimeout.String())
		if err != nil {
			return exitUsage, fmt.Errorf("question %s got no answer: %w", q.id, err)
		}
		answers = append(answers, testimony.Answer{ID: q.id, Question: q.text, Reply: strings.TrimSpace(reply)})
		log.qa(q.id, q.text, strings.TrimSpace(reply))
	}

	inspector := cliRunner{bin: *inspBin, profile: *profile}
	fmt.Printf("inspecting %s ...\n", title)
	reportJSON, err := inspector.run(time.Minute, "session", "context", title, "--json")
	if err != nil {
		return exitUsage, fmt.Errorf("the inspector could not read the probe: %w", err)
	}
	log.event("inspector report captured (%d bytes)", len(reportJSON))

	// The --item id is read out of the inspector's own document, never
	// constructed: the inspector records paths as it saw them, and an invented
	// id could name a row that is not there.
	var itemJSON []byte
	memID, memErr := testimony.FindProjectMemoryID([]byte(reportJSON), workdir)
	if memErr != nil {
		log.event("no --item fetched: %v", memErr)
	} else {
		out, err := inspector.run(time.Minute, "session", "context", title, "--item", memID, "--json")
		if err != nil {
			log.event("--item %s failed: %v", memID, err)
		} else {
			itemJSON = []byte(out)
			log.event("inspector --item %s captured (%d bytes)", memID, len(itemJSON))
		}
	}

	// Teardown happens BEFORE the artifact is written, because its outcome is
	// part of the artifact: a testimony run whose probe is still alive has not
	// finished, and the report must say so rather than leave it to memory.
	td := life.tearDown()
	log.event("teardown: stopped=%v removed=%v verified_gone=%v %s", td.Stopped, td.Removed, td.VerifiedGone, td.Detail)

	rep, err := testimony.Compare(testimony.Input{
		Slug: t.Slug,
		Probe: testimony.Probe{
			Title:        title,
			Workdir:      workdir,
			Harness:      "claude",
			LifecycleCLI: *adBin,
			InspectorCLI: *inspBin,
		},
		Recipe:         recipe,
		Answers:        answers,
		ReportJSON:     []byte(reportJSON),
		MemoryItemJSON: itemJSON,
		ResolvePath:    resolveExistingPath,
		Tool:           version,
		Now:            time.Now(),
	})
	if err != nil {
		return exitUsage, err
	}
	rep.Teardown = td

	dir := filepath.Join(t.Root(), testimony.Dir)
	if err := testimony.Write(dir, rep); err != nil {
		return exitUsage, err
	}
	if err := os.WriteFile(filepath.Join(dir, testimony.TranscriptFile), []byte(log.markdown(t.Slug, title)), 0o644); err != nil { //nolint:gosec // committed artifact
		return exitUsage, err
	}

	fmt.Printf("wrote %s, %s and %s\n",
		t.Rel(filepath.Join(dir, testimony.ReportJSONFile)),
		t.Rel(filepath.Join(dir, testimony.ReportMDFile)),
		t.Rel(filepath.Join(dir, testimony.TranscriptFile)))
	for _, r := range rep.Rows {
		fmt.Printf("  %-26s %-13s %s\n", r.ID, string(r.Verdict), condenseLine(r.Note))
	}
	fmt.Printf("teardown: stopped=%v removed=%v verified_gone=%v\n", td.Stopped, td.Removed, td.VerifiedGone)
	if !rep.Pass || !td.VerifiedGone {
		return exitGate, nil
	}
	return exitOK, nil
}

// probeQuestion pairs a stable id with the literal single-line message sent.
// Single-line is a hard requirement of the transport, not a style choice.
type probeQuestion struct {
	id   string
	text string
}

// probeQuestions builds the run's questions. Every one demands a QUOTE or a
// single committed word — checkable strings, never opinions — and each
// resolves to something the recipe planted, so the right answer is known
// before the probe is asked.
func probeQuestions(r testimony.Recipe) []probeQuestion {
	return []probeQuestion{
		{"q1", "G4B PROBE Q1: reply with exactly one line and nothing else: the verbatim first line of this project's CLAUDE.md."},
		{"q2", "G4B PROBE Q2: reply with one line and nothing else: the absolute paths of every CLAUDE.md or memory instruction file currently loaded in your context, separated by single spaces."},
		{"q3", fmt.Sprintf("G4B PROBE Q3: reply with exactly one word: LOADED if the full SKILL.md body of the skill named %s is in your context right now, or LISTED if you can only see its name and description in a skill catalogue.", r.SkillName)},
		{"q4", fmt.Sprintf("G4B PROBE Q4: reply with exactly one line and nothing else: the description of the %s skill exactly as it appears in your skill listing.", r.SkillName)},
	}
}

// writeRecipe lays down the probe's KNOWN context in a fresh /tmp directory:
// a project CLAUDE.md whose first line is a nonce-carrying beacon, and one
// skill whose description carries the same nonce. Everything a question asks
// about is planted here, which is what makes the testimony checkable.
func writeRecipe(slug string, r testimony.Recipe) (string, error) {
	workdir, err := os.MkdirTemp("/tmp", "sixgate-g4b-"+slug+"-")
	if err != nil {
		return "", err
	}
	claudeMD := r.FirstLine + "\n\n" +
		"This directory is a disposable SIXGATE G4b testimony-probe workspace; it is\n" +
		"deleted when the probe is torn down. When a message prefixed \"G4B PROBE\"\n" +
		"arrives, answer it literally and tersely in plain text, without running any\n" +
		"tools and without adding anything beyond what the question asks for.\n"
	if err := os.WriteFile(filepath.Join(workdir, "CLAUDE.md"), []byte(claudeMD), 0o644); err != nil { //nolint:gosec // throwaway recipe
		return "", err
	}
	skillDir := filepath.Join(workdir, ".claude", "skills", r.SkillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return "", err
	}
	skillMD := "---\nname: " + r.SkillName + "\ndescription: " + r.SkillDescription + "\n---\n\n" +
		"# " + r.SkillName + "\n\n" +
		"This body exists on disk and is NOT loaded at startup. If a probe's testimony\n" +
		"says the full body is in its context, either the harness changed its loading\n" +
		"model or the inspector's loaded-versus-listed model is wrong — both findings.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil { //nolint:gosec // throwaway recipe
		return "", err
	}
	return workdir, nil
}

func probeNonce() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// cliRunner shells out to an agent-deck binary. There is no shell anywhere in
// this path: every invocation is an argv assembled here, so nothing a probe
// replies can become a command.
type cliRunner struct {
	bin     string
	profile string
}

func (c cliRunner) run(timeout time.Duration, args ...string) (string, error) {
	argv := args
	if c.profile != "" {
		argv = append([]string{"-p", c.profile}, args...)
	}
	cmd := exec.Command(c.bin, argv...) //nolint:gosec // argv assembled above, no shell
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	timer := time.AfterFunc(timeout, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	err := cmd.Run()
	timer.Stop()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", filepath.Base(c.bin), strings.Join(argv, " "), err, msg)
	}
	return stdout.String(), nil
}

// probeLifecycle owns the disposable probe's mandatory ending. tearDown is
// idempotent so it can run both explicitly (its outcome belongs in the
// artifact) and deferred (so an error path can never leak the probe).
type probeLifecycle struct {
	cli      cliRunner
	title    string
	workdir  string
	keep     bool
	launched bool
	done     bool
	result   testimony.Teardown
}

func (p *probeLifecycle) tearDown() testimony.Teardown {
	if p.done {
		return p.result
	}
	p.done = true
	var details []string
	if p.launched {
		if _, err := p.cli.run(time.Minute, "session", "stop", p.title); err != nil {
			details = append(details, "stop: "+err.Error())
		} else {
			p.result.Stopped = true
		}
		if _, err := p.cli.run(time.Minute, "remove", p.title); err != nil {
			details = append(details, "remove: "+err.Error())
		} else {
			p.result.Removed = true
		}
		// "We asked for removal" and "it is gone" are different claims, and
		// only the second one is evidence: the fleet list has to say so.
		out, err := p.cli.run(time.Minute, "list", "--json")
		switch {
		case err != nil:
			details = append(details, "list: "+err.Error())
		case jsonNamesTitle([]byte(out), p.title):
			details = append(details, "the fleet list still names the probe after removal")
		default:
			p.result.VerifiedGone = true
			details = append(details, "the fleet list no longer names the probe")
		}
	} else {
		details = append(details, "nothing was launched, so there was nothing to tear down")
		p.result.VerifiedGone = true
	}
	if !p.keep {
		if err := os.RemoveAll(p.workdir); err != nil {
			details = append(details, "workdir: "+err.Error())
		}
	} else {
		details = append(details, "workdir kept: "+p.workdir)
	}
	p.result.Detail = strings.Join(details, "; ")
	return p.result
}

// jsonNamesTitle reports whether any object in a JSON document carries
// "title": <title>. It walks the decoded value rather than substring-matching
// the raw bytes, so encoder whitespace cannot hide a live probe.
func jsonNamesTitle(raw []byte, title string) bool {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// An unreadable list proves nothing, and "proves nothing" must fail
		// the verification, so report the title as possibly present.
		return true
	}
	var walk func(any) bool
	walk = func(n any) bool {
		switch x := n.(type) {
		case map[string]any:
			if s, ok := x["title"].(string); ok && s == title {
				return true
			}
			for _, c := range x {
				if walk(c) {
					return true
				}
			}
		case []any:
			for _, c := range x {
				if walk(c) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}

// resolveExistingPath canonicalises a path when it exists on this machine, so
// the comparator can treat two spellings of one file as one file. It lives in
// the driver because the comparator is forbidden the filesystem.
func resolveExistingPath(p string) string {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return resolved
}

// transcript accumulates the run's narration for transcript.md: the questions
// and replies verbatim, and the lifecycle events with their clock.
type transcript struct {
	lines []string
}

func (t *transcript) stamp() string { return time.Now().UTC().Format("15:04:05") }

func (t *transcript) event(format string, args ...any) {
	t.lines = append(t.lines, fmt.Sprintf("- `%s` %s", t.stamp(), fmt.Sprintf(format, args...)))
}

func (t *transcript) qa(id, question, reply string) {
	t.lines = append(t.lines,
		fmt.Sprintf("- `%s` **%s asked and answered**", t.stamp(), id),
		"",
		fmt.Sprintf("  **%s:** %s", id, question),
		"",
		"  ```text",
		indentBlock(reply),
		"  ```",
		"")
}

func (t *transcript) markdown(slug, title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# G4b probe transcript — %s — probe `%s`\n\n", slug, title)
	fmt.Fprintf(&b, "> %s\n\n", testimony.ScopeNote)
	b.WriteString("Questions and replies verbatim, with the lifecycle around them. Timestamps are UTC.\n\n")
	for _, l := range t.lines {
		b.WriteString(l + "\n")
	}
	return b.String()
}

func indentBlock(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func condenseLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 110 {
		return s[:107] + "…"
	}
	return s
}
