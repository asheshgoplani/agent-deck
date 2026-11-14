package ask

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// askLog writes the request lifecycle to the debug log (AGENTDECK_DEBUG):
// ids, kinds, names, states and who acted. Never a value, an answer or a
// nonce.
var askLog = logging.ForComponent(logging.CompAsk)

// Request limits.
const (
	DefaultWait    = 10 * time.Minute
	MaxWait        = 30 * time.Minute
	MinWait        = time.Second
	MaxSecretBytes = 64 << 10
	maxSeen        = 16
)

// SeenHold is how long one seen hides a pending request from the TUI (an app
// seen) and from the asker's terminal fallback (any seen). The app's seen
// arrives over the ungated `events publish`, so a forged one, or a dialog
// that never answers, must not hide the request until it times out: with no
// deliver or cancel within SeenHold of the seen, the TUI and the fallback
// show it anyway.
const SeenHold = 30 * time.Second

// AppShowing reports whether the app marked r seen less than SeenHold before
// at (the TUI leaves such a request to the app).
func AppShowing(r Record, at time.Time) bool {
	for _, s := range r.Seen {
		if s.By == "app" && at.Sub(s.At) < SeenHold {
			return true
		}
	}
	return false
}

// seenHolding reports whether any UI marked r seen less than SeenHold
// before at (the asker holds its terminal fallback back).
func seenHolding(r Record, at time.Time) bool {
	for _, s := range r.Seen {
		if at.Sub(s.At) < SeenHold {
			return true
		}
	}
	return false
}

// UIs that may show, answer or cancel a request (Header.AnsweredBy, MarkSeen).
var uiNames = []string{"app", "tui", "terminal"}

func isUI(by string) bool { return slices.Contains(uiNames, by) }

// NewRequest is what an asker asks for. Text fields are untrusted: Create
// sanitizes and caps them.
type NewRequest struct {
	Kind           Kind
	SessionID      string
	SessionTitle   string
	Host           string
	Name           string   // secret: scope/NAME
	Question       string   // choice, text
	Options        []string // choice: 2 to 6
	Reason         string
	Replace        bool // secret: the name exists and the owner is told it is replaced
	CallerVerified bool // the caller's /proc parent chain reaches the session's pane
	Sandboxed      bool // the session runs in a sandbox
	Wait           time.Duration
	PID            int // the asker's pid (default: this process)
}

// Header is what a UI displayed, sent ahead of the value (`ask deliver`
// stdin line 1). Deliver refuses unless every field equals the record and
// Nonce is the one that UI announced in macapp.ask.seen.
type Header struct {
	RequestID  string `json:"request_id"`
	Kind       Kind   `json:"kind"`
	Name       string `json:"name"`
	Replace    bool   `json:"replace"`
	Nonce      string `json:"nonce"`
	AnsweredBy string `json:"answered_by"`
}

// Result is the outcome of Deliver or Cancel. It never carries the value or
// the answer.
type Result struct {
	Status     State  `json:"status"`
	Replaced   string `json:"replaced,omitempty"`
	AnsweredBy string `json:"answered_by,omitempty"`
}

func refuse(base *Refusal, format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{base}, args...)...)
}

func buildRecord(profile string, req NewRequest, cfg settings) (Record, error) {
	wait := req.Wait
	if wait == 0 {
		wait = DefaultWait
	}
	if wait < MinWait || wait > MaxWait {
		return Record{}, refuse(ErrInvalid, "--wait must be between %s and %s", MinWait, MaxWait)
	}
	if req.SessionID == "" || len(req.SessionID) > 128 || SanitizeText(req.SessionID, 0) != req.SessionID {
		return Record{}, refuse(ErrInvalid, "a request needs the asking session")
	}
	if req.Sandboxed && !cfg.allowSandboxed {
		return Record{}, ErrSandboxed
	}
	id, err := newID()
	if err != nil {
		return Record{}, err
	}
	t := now().UTC()
	rec := Record{
		RequestID:      id,
		Kind:           req.Kind,
		SessionID:      req.SessionID,
		SessionTitle:   SanitizeText(req.SessionTitle, maxTitle),
		Host:           SanitizeText(req.Host, maxHost),
		Profile:        profile,
		Reason:         SanitizeText(req.Reason, MaxReason),
		Replace:        req.Replace,
		CallerVerified: req.CallerVerified,
		RequestedAt:    t,
		ExpiresAt:      t.Add(wait),
		PID:            req.PID,
		State:          StatePending,
	}
	if rec.PID == 0 {
		rec.PID = os.Getpid()
	}
	switch req.Kind {
	case KindSecret:
		if err := ValidateName(req.Name); err != nil {
			return Record{}, err
		}
		if !ScopeAllowed(req.Name, cfg.scopes) {
			return Record{}, refuse(ErrScope, "%s", req.Name)
		}
		rec.Name = req.Name
	case KindChoice, KindText:
		if req.Name != "" || req.Replace {
			return Record{}, refuse(ErrInvalid, "only a secret request has a name")
		}
		rec.Question = SanitizeText(req.Question, MaxQuestion)
		if rec.Question == "" {
			return Record{}, refuse(ErrInvalid, "a %s request needs a question", req.Kind)
		}
		if req.Kind == KindText {
			if len(req.Options) > 0 {
				return Record{}, refuse(ErrInvalid, "a text request has no options")
			}
			break
		}
		if len(req.Options) < 2 || len(req.Options) > MaxOptions {
			return Record{}, refuse(ErrInvalid, "a choice needs 2 to %d options", MaxOptions)
		}
		for i, o := range req.Options {
			if o == "" || utf8.RuneCountInString(o) > MaxOption || SanitizeText(o, 0) != o {
				return Record{}, refuse(ErrInvalid, "option %d must be one line of plain text, at most %d characters", i+1, MaxOption)
			}
			if slices.Contains(req.Options[:i], o) {
				return Record{}, refuse(ErrInvalid, "option %q is given twice", o)
			}
		}
		rec.Options = slices.Clone(req.Options)
	default:
		return Record{}, refuse(ErrInvalid, "kind must be secret, choice or text")
	}
	return rec, nil
}

// Create validates req and writes a pending record: name and scope, one
// open request per session, the rate limits, and for a secret whether the
// name exists in the vault (refused unless Replace, and Replace refused when
// it does not exist). It does not announce the request; Ask does.
func Create(profile string, req NewRequest) (Record, error) {
	cfg := loadSettings()
	if !cfg.enabled {
		return Record{}, ErrDisabled
	}
	rec, err := buildRecord(profile, req, cfg)
	if err != nil {
		return Record{}, err
	}
	if rec.Kind == KindSecret {
		exists, err := vaultExists(rec.Name, rec.SessionID)
		if err != nil {
			return Record{}, err
		}
		if exists && !rec.Replace {
			return Record{}, refuse(ErrExists, "%s", rec.Name)
		}
		if !exists && rec.Replace {
			return Record{}, refuse(ErrNotExists, "%s", rec.Name)
		}
	}
	dir, err := openDir(profile, true)
	if err != nil {
		return Record{}, err
	}
	var closed []Record
	err = func() error {
		unlock, err := lockDir(dir)
		if err != nil {
			return err
		}
		defer unlock()
		for _, r := range sweep(dir, loadAll(dir)) {
			if r.State != StatePending || r.SessionID != rec.SessionID {
				continue
			}
			switch {
			case !now().Before(r.ExpiresAt):
				r.State, r.AnsweredBy = StateExpired, "asker"
			case !pidAlive(r.PID):
				r.State, r.AnsweredBy = StateCancelled, "asker"
			default:
				return refuse(ErrOneOpen, "%s", r.RequestID)
			}
			r.FinishedAt = now().UTC()
			if err := writeRecord(dir, r); err != nil {
				return err
			}
			closed = append(closed, r)
		}
		rate := readRate(dir)
		if err := rate.allow(rec.SessionID); err != nil {
			return err
		}
		if err := writeRecord(dir, rec); err != nil {
			return err
		}
		rate.add(rec.SessionID)
		return writeRate(dir, rate)
	}()
	for _, r := range closed {
		publishAck(nil, r)
	}
	if err != nil {
		askLog.Debug("ask_refused", slog.String("kind", string(rec.Kind)), slog.String("session_id", rec.SessionID), slog.String("error", err.Error()))
		return Record{}, err
	}
	askLog.Debug("ask_created", slog.String("request_id", rec.RequestID), slog.String("kind", string(rec.Kind)),
		slog.String("name", rec.Name), slog.String("session_id", rec.SessionID), slog.Bool("replace", rec.Replace),
		slog.Bool("caller_verified", rec.CallerVerified))
	return rec, nil
}

// update applies fn to the record under the dir lock and writes it when fn
// returns nil. Deliver holds the same lock while the vault runs.
func update(profile, id string, fn func(*Record) error) (Record, error) {
	dir, err := openDir(profile, false)
	if err != nil {
		return Record{}, err
	}
	if dir == "" {
		return Record{}, ErrNotFound
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	rec, err := readRecord(dir, id)
	if err != nil {
		return Record{}, err
	}
	if err := fn(&rec); err != nil {
		return rec, err
	}
	return rec, writeRecord(dir, rec)
}

// errFinal marks an update whose record was changed (expired) and must be
// written although the call fails.
type errFinal struct{ err error }

func (e errFinal) Error() string { return e.err.Error() }
func (e errFinal) Unwrap() error { return e.err }

// updateFinal is update, except that fn may return errFinal to write the
// record and still fail (an expired request is marked expired on refusal).
func updateFinal(profile, id string, fn func(*Record) error) (Record, error) {
	var final error
	rec, err := update(profile, id, func(r *Record) error {
		err := fn(r)
		var f errFinal
		if errors.As(err, &f) {
			final = f.err
			return nil
		}
		return err
	})
	if err == nil && final != nil {
		return rec, final
	}
	return rec, err
}

// expireIfDue marks a pending record past its deadline expired.
func expireIfDue(r *Record) error {
	if r.State == StatePending && !now().Before(r.ExpiresAt) {
		r.State, r.AnsweredBy, r.FinishedAt = StateExpired, "asker", now().UTC()
		return errFinal{ErrExpired}
	}
	return nil
}

func seenMatches(r Record, by, nonce string) bool {
	for _, s := range r.Seen {
		if s.By == by && s.Nonce == nonce {
			return true
		}
	}
	return false
}

func headerMatches(r Record, h Header) bool {
	return h.RequestID == r.RequestID && h.Kind == r.Kind && h.Name == r.Name && h.Replace == r.Replace &&
		isUI(h.AnsweredBy) && validID(h.Nonce) && seenMatches(r, h.AnsweredBy, h.Nonce)
}

// Deliver stores a secret in the vault or records a choice/text answer. It
// re-reads the record under the lock and refuses unless the request is
// pending and not expired, the header equals the record (with the nonce the
// answering UI announced), and for a secret the name is still in an allowed
// scope and its existence in the vault still matches Replace. The vault gets
// the value on stdin with a clean environment; value is zeroed before
// Deliver returns. A choice must be one of the options; a text answer is one
// line of at most MaxTextAnswer bytes that does not look like a credential.
func Deliver(profile string, h Header, value []byte) (Result, error) {
	defer clear(value)
	cfg := loadSettings()
	if !cfg.enabled {
		return Result{}, ErrDisabled
	}
	v := value
	if n := len(v); n > 0 && v[n-1] == '\n' {
		v = v[:n-1]
	}
	rec, err := updateFinal(profile, h.RequestID, func(r *Record) error {
		if r.State != StatePending {
			return ErrNotPending
		}
		if err := expireIfDue(r); err != nil {
			return err
		}
		if !headerMatches(*r, h) {
			return ErrHeaderMismatch
		}
		switch r.Kind {
		case KindSecret:
			if !ScopeAllowed(r.Name, cfg.scopes) {
				return ErrScope
			}
			if len(v) == 0 {
				return ErrEmpty
			}
			if len(v) > MaxSecretBytes {
				return ErrTooLarge
			}
			exists, err := vaultExists(r.Name, r.SessionID)
			if err != nil {
				return err
			}
			if exists != r.Replace {
				return ErrReplaceMismatch
			}
			fallback := "no"
			if r.Replace {
				fallback = "yes"
			}
			replaced, err := vaultSet(r.Name, r.SessionID, v, fallback)
			if err != nil {
				return err
			}
			r.State, r.Replaced = StateStored, replaced
		default:
			answer, err := checkAnswer(*r, v)
			if err != nil {
				return err
			}
			r.State, r.Answer, r.Replaced = StateAnswered, answer, "no"
		}
		r.AnsweredBy, r.FinishedAt = h.AnsweredBy, now().UTC()
		return nil
	})
	if errors.Is(err, ErrExpired) {
		publishAck(nil, rec)
	}
	if err != nil {
		askLog.Debug("ask_deliver_refused", slog.String("request_id", h.RequestID), slog.String("answered_by", h.AnsweredBy), slog.String("error", err.Error()))
		return Result{}, err
	}
	publishAck(nil, rec)
	askLog.Debug("ask_delivered", slog.String("request_id", rec.RequestID), slog.String("status", string(rec.State)),
		slog.String("answered_by", rec.AnsweredBy), slog.String("replaced", rec.Replaced))
	return Result{Status: rec.State, Replaced: rec.Replaced, AnsweredBy: rec.AnsweredBy}, nil
}

func checkAnswer(r Record, v []byte) (string, error) {
	if len(v) == 0 {
		return "", ErrEmpty
	}
	if len(v) > MaxTextAnswer {
		return "", refuse(ErrBadAnswer, "over %d bytes", MaxTextAnswer)
	}
	if !utf8.Valid(v) || bytes.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", refuse(ErrBadAnswer, "one line of text only")
	}
	answer := string(v)
	if r.Kind == KindChoice && !slices.Contains(r.Options, answer) {
		return "", refuse(ErrBadAnswer, "not one of the options")
	}
	if LooksLikeCredential(answer) {
		return "", ErrLooksLikeSecret
	}
	return answer, nil
}

// Cancel ends a pending request (state cancelled) and publishes the ack, so
// every dialog closes and the asker exits 3. by is app, tui or terminal and
// nonce must be the one that UI announced (MarkSeen / macapp.ask.seen): a
// cancel without it is refused, so nobody can end a request a dialog shows.
// The asker's own cancel (Ctrl-C, --no-fallback) happens in Ask.
func Cancel(profile, id, by, nonce string) (Result, error) {
	if !isUI(by) {
		return Result{}, refuse(ErrInvalid, "--by must be app, tui or terminal")
	}
	if nonce == "" {
		return Result{}, ErrNonceRequired
	}
	return cancel(profile, id, by, func(r *Record) error {
		if !seenMatches(*r, by, nonce) {
			return ErrHeaderMismatch
		}
		return nil
	})
}

// CancelUnshown cancels a request no UI showed (the app's "mute" of a
// queued request): refused once any UI recorded a seen for it. The record
// notes it (cancelled_unshown).
func CancelUnshown(profile, id, by string) (Result, error) {
	if !isUI(by) {
		return Result{}, refuse(ErrInvalid, "--by must be app, tui or terminal")
	}
	return cancel(profile, id, by, func(r *Record) error {
		if len(r.Seen) > 0 {
			return ErrAlreadyShown
		}
		r.CancelledUnshown = true
		return nil
	})
}

// cancelByAsker is the asker's own cancel (interrupt, --no-fallback).
func cancelByAsker(profile, id string) (Result, error) {
	return cancel(profile, id, "asker", nil)
}

func cancel(profile, id, by string, check func(*Record) error) (Result, error) {
	rec, err := updateFinal(profile, id, func(r *Record) error {
		if r.State != StatePending {
			return ErrNotPending
		}
		if err := expireIfDue(r); err != nil {
			return err
		}
		if check != nil {
			if err := check(r); err != nil {
				return err
			}
		}
		r.State, r.AnsweredBy, r.FinishedAt = StateCancelled, by, now().UTC()
		return nil
	})
	if err != nil && !errors.Is(err, ErrExpired) {
		return Result{}, err
	}
	publishAck(nil, rec)
	if err != nil {
		return Result{}, err
	}
	askLog.Debug("ask_cancelled", slog.String("request_id", rec.RequestID), slog.String("by", by), slog.Bool("unshown", rec.CancelledUnshown))
	return Result{Status: StateCancelled, AnsweredBy: by}, nil
}

// MarkSeen records that a UI (tui or terminal; the app announces over the
// bus) shows the request, with a fresh nonce the UI must send in the deliver
// header, and publishes macapp.ask.seen.
func MarkSeen(profile, id, by string) (string, error) {
	if !isUI(by) {
		return "", refuse(ErrInvalid, "by must be app, tui or terminal")
	}
	nonce, err := newID()
	if err != nil {
		return "", err
	}
	rec, err := recordSeen(profile, id, by, nonce)
	if err != nil {
		return "", err
	}
	publish(nil, rec.Profile, events.KindMacappAskSeen, rec.SessionID, seenData{RequestID: rec.RequestID, AskEventID: rec.AskEventID, Nonce: nonce, By: by})
	askLog.Debug("ask_marked_seen", slog.String("request_id", rec.RequestID), slog.String("by", by))
	return nonce, nil
}

func recordSeen(profile, id, by, nonce string) (Record, error) {
	if !isUI(by) || !validID(nonce) {
		return Record{}, ErrInvalid
	}
	return update(profile, id, func(r *Record) error {
		if r.State != StatePending {
			return ErrNotPending
		}
		if !now().Before(r.ExpiresAt) {
			return ErrExpired
		}
		if seenMatches(*r, by, nonce) {
			return nil
		}
		if len(r.Seen) >= maxSeen {
			return refuse(ErrInvalid, "shown by too many dialogs")
		}
		r.Seen = append(r.Seen, Seen{By: by, Nonce: nonce, At: now().UTC()})
		if !slices.Contains(r.SeenBy, by) {
			r.SeenBy = append(r.SeenBy, by)
		}
		return nil
	})
}

// Show returns one request without its answer or nonces.
func Show(profile, id string) (Record, error) {
	rec, err := readAnswered(profile, id)
	if err != nil {
		return Record{}, err
	}
	return rec.public(), nil
}

// readAnswered reads the full record (with its answer, for the asker).
func readAnswered(profile, id string) (Record, error) {
	dir, err := openDir(profile, false)
	if err != nil {
		return Record{}, err
	}
	if dir == "" {
		return Record{}, ErrNotFound
	}
	return readRecord(dir, id)
}

// List returns the pending, unexpired requests of profile, oldest first,
// without answers or nonces. A profile with no ask dir has none.
func List(profile string) ([]Record, error) {
	all, err := ListAll(profile)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range all {
		if r.State == StatePending {
			out = append(out, r)
		}
	}
	return out, nil
}

// ListAll is List including finished requests still kept (24 h).
func ListAll(profile string) ([]Record, error) {
	dir, err := openDir(profile, false)
	if err != nil || dir == "" {
		return nil, err
	}
	var out []Record
	for _, r := range loadAll(dir) {
		out = append(out, r.public())
	}
	return out, nil
}

// ClearAnswer drops a choice/text answer from the record once the asker
// printed it.
func ClearAnswer(profile, id string) error {
	_, err := update(profile, id, func(r *Record) error {
		r.Answer = ""
		return nil
	})
	return err
}

// Frame payloads (PLAN.md section 12). None has a value or answer field.
type askData struct {
	RequestID      string   `json:"request_id"`
	Kind           Kind     `json:"kind"`
	Name           string   `json:"name"`
	Question       string   `json:"question"`
	Options        []string `json:"options"`
	Reason         string   `json:"reason"`
	Replace        bool     `json:"replace"`
	SessionTitle   string   `json:"session_title"`
	Host           string   `json:"host"`
	Profile        string   `json:"profile"`
	CallerVerified bool     `json:"caller_verified"`
	RequestedAt    string   `json:"requested_at"`
	ExpiresAt      string   `json:"expires_at"`
}

type seenData struct {
	RequestID  string `json:"request_id"`
	AskEventID string `json:"ask_event_id"`
	Nonce      string `json:"nonce"`
	By         string `json:"by"`
}

type ackData struct {
	RequestID  string `json:"request_id"`
	AskEventID string `json:"ask_event_id"`
	Status     State  `json:"status"`
	AnsweredBy string `json:"answered_by"`
	Replaced   string `json:"replaced"`
}

func announceData(r Record) askData {
	opts := r.Options
	if opts == nil {
		opts = []string{}
	}
	return askData{
		RequestID: r.RequestID, Kind: r.Kind, Name: r.Name, Question: r.Question, Options: opts, Reason: r.Reason,
		Replace: r.Replace, SessionTitle: r.SessionTitle, Host: r.Host, Profile: r.Profile, CallerVerified: r.CallerVerified,
		RequestedAt: r.RequestedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: r.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}

const flushTimeout = 10 * time.Second

// publish commits one frame on bus, or on a bus opened for profile when bus
// is nil, and returns its event id ("" when the bus is off or did not commit).
func publish(bus *events.Bus, profile, kind, sessionID string, data any) string {
	if bus == nil {
		bus = events.OpenProfile(profile)
		defer bus.Close()
	}
	if !bus.Stats().Enabled {
		return ""
	}
	id := bus.PublishWithID(kind, sessionID, data)
	if id == "" || !bus.Flush(flushTimeout) {
		return ""
	}
	return id
}

func publishAck(bus *events.Bus, r Record) {
	if r.RequestID == "" || r.State == StatePending {
		return
	}
	replaced := r.Replaced
	if replaced == "" {
		replaced = "no"
	}
	publish(bus, r.Profile, events.KindMacappAskAck, r.SessionID, ackData{RequestID: r.RequestID, AskEventID: r.AskEventID, Status: r.State, AnsweredBy: r.AnsweredBy, Replaced: replaced})
}
