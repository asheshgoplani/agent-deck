// Package ask lets an agent ask the owner for a secret, a choice or a short
// text through a dialog (Mac app, TUI, else a terminal command), so that a
// secret goes from the dialog to the host vault and never into a model
// context: not the asking agent's process, pane, transcript, hooks, events,
// inbox, logs or argv (docs/ask.md, ASK-117 PLAN.md sections 11 and 12).
//
// # In-process API (for the TUI)
//
// Every function takes a resolved profile (session.GetEffectiveProfile /
// ResolveProfileForStorage), never the raw -p string.
//
//	ask.List(profile) ([]ask.Record, error)
//	    Pending, unexpired requests, oldest first. No answers, no nonces.
//	ask.ListAll(profile) ([]ask.Record, error)
//	    The same including finished requests still kept (24 h).
//	ask.Show(profile, id) (ask.Record, error)
//	    One request; a pending one past expires_at reads as expired.
//	ask.MarkSeen(profile, id, by) (nonce string, err error)
//	    Call when the modal is on screen (by "tui"). Records the UI and a
//	    fresh nonce in the request and publishes macapp.ask.seen. Keep the
//	    nonce for Deliver/Cancel.
//	ask.Deliver(profile, ask.Header{RequestID, Kind, Name, Replace, Nonce,
//	        AnsweredBy: "tui"}, value []byte) (ask.Result, error)
//	    Header holds exactly what the modal showed. It holds the ask lock
//	    while the vault runs (up to 30 s): call it from a tea.Cmd, never
//	    from Update. A secret goes to the vault
//	    on the child's stdin; value is zeroed before Deliver returns (keep it
//	    a []byte, never a string). Result{Status: stored|answered,
//	    Replaced: yes|no}; it never carries the value.
//	ask.Cancel(profile, id, by, nonce) (ask.Result, error)
//	    The modal's Cancel/Esc (by "tui", the nonce from MarkSeen; a cancel
//	    without the nonce is refused with ErrNonceRequired).
//	ask.CancelUnshown(profile, id, by) (ask.Result, error)
//	    Cancel a request no UI showed; refused (ErrAlreadyShown) once any
//	    UI recorded a seen. The record notes cancelled_unshown.
//	ask.AppShowing(r, now) bool
//	    The app marked r seen less than ask.SeenHold (30 s) ago: leave it to
//	    the app. Older seens with no deliver or cancel no longer count.
//	ask.SanitizeText(s, max) string
//	    Records are already sanitized; use it for anything else shown.
//
// Errors: ask.IsRefusal(err) for a refusal with a fixed, value-free message
// (show err.Error()); errors.Is against ErrExpired, ErrNotPending,
// ErrHeaderMismatch, ErrLooksLikeSecret, ErrVaultRefused and the others in
// errors.go. Tests in other packages swap in a fake vault with
// ask.SetVaultForTest(path), which panics outside a test binary.
//
// # Host side
//
// The asker (`agent-deck ask secret|choice|text`) calls Ask, which runs
// Create (validation, scopes, one open request per session, rate limits,
// vault exists/--replace), publishes macapp.ask and waits: seen frames from
// the app are recorded, expiry and Ctrl-C end the request with an ack. The
// vault is the fixed path /usr/local/bin/deck-secret, checked root-owned and
// not group/world-writable before each run, started with a clean
// environment (PATH, HOME, AGENTDECK_SESSION_ID). Records live in
// <profile>/ask/<request_id>.json (0600, dir 0700, read with O_NOFOLLOW,
// written by rename under a flock on ask/.lock) and never hold a value.
package ask
