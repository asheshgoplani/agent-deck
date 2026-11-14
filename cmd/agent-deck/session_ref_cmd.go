package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sessionref"
)

// handleSessionRef implements `agent-deck session ref <source> <target>`
// (ASK-111): type a short reference to session A into session B's composer,
// never pressing Enter, or with --print only print it. The work is
// sessionref.Hand, the same call the TUI's pick/drop makes.
func handleSessionRef(profile string, args []string) {
	fs := flag.NewFlagSet("session ref", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	printOnly := fs.Bool("print", false, "Only print the reference; type nothing (the target need not run)")
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("q", false, "Quiet mode (exit code only)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session ref <source> <target> [--print] [--json]")
		fmt.Println()
		fmt.Println("Hand <target> a short reference to <source>: its title, id, project path and")
		fmt.Println("status, the command that reads it (agent-deck session output <id> -q) and its")
		fmt.Println("transcript path when known. Nothing moves and <source>'s output is not pasted.")
		fmt.Println()
		fmt.Println("The reference is typed into <target>'s composer as one line and is NOT sent:")
		fmt.Println("press Enter in <target> yourself. --print types nothing and prints it instead.")
		fmt.Println()
		fmt.Println("Refused: the same session twice, sessions in different profiles, remote (SSH)")
		fmt.Println("sessions, and a target that is not running (except with --print).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  agent-deck session ref auth-fix reviewer          # type the reference into reviewer")
		fmt.Println("  agent-deck session ref auth-fix reviewer --print  # just print it")
		fmt.Println("  agent-deck session ref a1b2c3d4 e5f6a7b8 --json")
	}

	if err := parseCLIFlags(fs, normalizeArgs(fs, args)); err != nil {
		exitCLI(1)
	}
	out := NewCLIOutput(*jsonOutput, *quiet)
	refuseRef := func(code, errCode, msg string, exit int) {
		if *jsonOutput {
			out.printJSON(map[string]interface{}{
				"ok": false, "success": false,
				"error_code": code, "code": errCode, "error": msg,
			})
		} else {
			fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		}
		exitCLI(exit)
	}

	if fs.NArg() != 2 {
		if !*jsonOutput {
			fs.Usage()
		}
		refuseRef(sessionref.CodeMissingSession, ErrCodeInvalidOperation, "session ref needs exactly two sessions: <source> <target>", 1)
		return
	}
	srcRef, dstRef := fs.Arg(0), fs.Arg(1)

	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		refuseRef(sessionref.CodeMissingSession, ErrCodeNotFound, err.Error(), 1)
		return
	}
	effective := storage.Profile()
	_ = storage.Close()

	resolve := func(ref, role string) *session.Instance {
		inst, errMsg, errCode := ResolveSession(ref, instances)
		if inst != nil {
			return inst
		}
		if errCode == ErrCodeNotFound {
			if other := profileHolding(ref, effective); other != "" {
				refuseRef(sessionref.CodeCrossProfile, ErrCodeInvalidOperation,
					fmt.Sprintf("%s %q is in profile %q, not %q: a reference only works inside one profile", role, ref, other, effective), 1)
				return nil
			}
			refuseRef(sessionref.CodeMissingSession, ErrCodeNotFound, fmt.Sprintf("%s: %s", role, errMsg), 2)
			return nil
		}
		refuseRef(sessionref.CodeMissingSession, errCode, fmt.Sprintf("%s: %s", role, errMsg), 1)
		return nil
	}
	src := resolve(srcRef, "source")
	dst := resolve(dstRef, "target")
	if src == nil || dst == nil {
		return
	}

	res, err := sessionref.Hand(
		sessionref.Endpoint{Inst: src, Profile: effective},
		sessionref.Endpoint{Inst: dst, Profile: effective},
		sessionref.Options{PrintOnly: *printOnly},
	)
	if err != nil {
		var r *sessionref.Refusal
		code := sessionref.CodeInsertFailed
		if errors.As(err, &r) {
			code = r.Code
		}
		errCode := ErrCodeInvalidOperation
		if code == sessionref.CodeInsertFailed {
			errCode = ErrCodeDeliveryFailed
		}
		refuseRef(code, errCode, err.Error(), 1)
		return
	}

	if *jsonOutput {
		out.printJSON(struct {
			OK      bool `json:"ok"`
			Success bool `json:"success"`
			*sessionref.Result
		}{true, true, res})
		return
	}
	if *quiet {
		return
	}
	fmt.Println(res.Text)
	if res.Inserted {
		fmt.Printf("%s typed into %q, not sent: press Enter there to send it\n", successSymbol, res.Target.Title)
	}
}

// profileHolding names another profile whose sessions resolve ref, or "".
// It runs only after ref failed to resolve in the current profile, to turn
// "not found" into the clearer cross-profile refusal.
func profileHolding(ref, current string) string {
	profiles, err := session.ListProfiles()
	if err != nil {
		return ""
	}
	for _, p := range profiles {
		if p == current {
			continue
		}
		storage, instances, _, err := loadSessionData(p)
		if err != nil {
			continue
		}
		_ = storage.Close()
		if inst, _, _ := ResolveSession(ref, instances); inst != nil {
			return p
		}
	}
	return ""
}
