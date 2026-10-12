package macapp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Frame kinds of the remote browser contract. They alias the built-in Mac
// app kinds in internal/events so the strings live in one place.
const (
	KindBrowserRequest  = events.KindMacappBrowserRequest
	KindBrowserResponse = events.KindMacappBrowserResponse
	KindAppFocus        = events.KindMacappAppFocus
	KindSessionClosed   = events.KindMacappSessionClosed
)

// ProtocolVersion is the v field of browser requests and responses.
const ProtocolVersion = 1

// Size caps. MaxShotBytes applies to the decoded image.
const (
	MaxTypeTextBytes = 8 << 10
	MaxReadTextBytes = 256 << 10
	MaxShotBytes     = 3 << 19 // 1.5 MiB
)

// Op is a browser operation.
type Op string

const (
	OpOpen  Op = "open"
	OpTabs  Op = "tabs"
	OpClose Op = "close"
	OpClick Op = "click"
	OpType  Op = "type"
	OpRead  Op = "read"
	OpShot  Op = "shot"
)

// Ops lists every operation in contract order.
var Ops = []Op{OpOpen, OpTabs, OpClose, OpClick, OpType, OpRead, OpShot}

// Valid reports whether op is part of the contract.
func (op Op) Valid() bool {
	return slices.Contains(Ops, op)
}

// Read modes.
const (
	ReadModeText    = "text"
	ReadModeOutline = "outline"
)

// Error codes carried in BrowserResponse.Error.Code.
const (
	CodeNotConnected   = "not_connected"
	CodeOwnerDriving   = "owner_driving"
	CodeNotYourTab     = "not_your_tab"
	CodeBadArgs        = "bad_args"
	CodeUnknownSession = "unknown_session"
	CodeRefusedURL     = "refused_url"
)

// BrowserArgs are the op arguments. Each op accepts only its own fields.
type BrowserArgs struct {
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	TabID    string `json:"tab_id,omitempty"`
	NewTab   bool   `json:"new_tab,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Selector string `json:"selector,omitempty"`
	Text     string `json:"text,omitempty"`
	Submit   bool   `json:"submit,omitempty"`
	Mode     string `json:"mode,omitempty"`
	FullPage bool   `json:"full_page,omitempty"`
}

// BrowserRequest is the data of a macapp.browser.request frame. The frame's
// session_id names the calling session.
type BrowserRequest struct {
	V           int         `json:"v"`
	ReqID       string      `json:"req_id"`
	Op          Op          `json:"op"`
	Args        BrowserArgs `json:"args"`
	Host        string      `json:"host"`
	RequestedAt time.Time   `json:"requested_at"`
	ExpiresAt   time.Time   `json:"expires_at"`
}

// ErrorInfo is a refusal or failure reported by the app.
type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// BrowserResponse is the data of a macapp.browser.response frame.
// RequestEventID echoes the request frame's event_id.
type BrowserResponse struct {
	V              int             `json:"v"`
	ReqID          string          `json:"req_id"`
	RequestEventID string          `json:"request_event_id"`
	OK             bool            `json:"ok"`
	Error          *ErrorInfo      `json:"error,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Driver         string          `json:"driver,omitempty"`
	AppID          string          `json:"app_id,omitempty"`
}

// Driver values.
const (
	DriverAgent = "agent"
	DriverOwner = "owner"
)

// PageResult is the result of open, click and type.
type PageResult struct {
	TabID string `json:"tab_id,omitempty"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

// Tab is one entry of a tabs result.
type Tab struct {
	TabID  string `json:"tab_id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// TabsResult is the result of tabs.
type TabsResult struct {
	Tabs   []Tab  `json:"tabs"`
	Driver string `json:"driver,omitempty"`
}

// ReadResult is the result of read.
type ReadResult struct {
	URL   string          `json:"url"`
	Title string          `json:"title"`
	Text  string          `json:"text"`
	Refs  json.RawMessage `json:"refs,omitempty"`
}

// ShotResult is the result of shot.
type ShotResult struct {
	MIME     string `json:"mime"`
	ImageB64 string `json:"image_b64"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// Validate checks the request envelope and the op's arguments.
func (r BrowserRequest) Validate() error {
	if r.V != ProtocolVersion {
		return fmt.Errorf("unsupported version %d", r.V)
	}
	if strings.TrimSpace(r.ReqID) == "" {
		return errors.New("req_id is required")
	}
	if r.RequestedAt.IsZero() || !r.ExpiresAt.After(r.RequestedAt) {
		return errors.New("expires_at must be after requested_at")
	}
	return r.Args.Validate(r.Op)
}

// Validate checks args for op: required fields, mutually exclusive targets,
// size caps and the open URL and path rules. Fields of other ops are refused.
func (a BrowserArgs) Validate(op Op) error {
	if !op.Valid() {
		return fmt.Errorf("unknown op %q", op)
	}
	if extra := a.fieldsOutside(op); len(extra) > 0 {
		return fmt.Errorf("%s does not take %s", op, strings.Join(extra, ", "))
	}
	for name, value := range map[string]string{"tab_id": a.TabID, "profile": a.Profile, "ref": a.Ref, "selector": a.Selector} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%s contains control characters", name)
		}
	}
	switch op {
	case OpOpen:
		if (a.URL == "") == (a.Path == "") {
			return errors.New("open needs exactly one of url or path")
		}
		if a.TabID != "" && a.NewTab {
			return errors.New("open takes tab_id or new_tab, not both")
		}
		if a.URL != "" {
			if _, err := ValidateURL(a.URL); err != nil {
				if errors.Is(err, ErrNotURL) {
					return errors.New("url must be an absolute http or https URL")
				}
				return err
			}
		}
		if a.Path != "" {
			return validatePagePath(a.Path)
		}
	case OpClose:
		if a.TabID == "" {
			return errors.New("close needs tab_id")
		}
	case OpClick:
		if countSet(a.Ref, a.Selector, a.Text) != 1 {
			return errors.New("click needs exactly one of ref, selector or text")
		}
	case OpType:
		if countSet(a.Ref, a.Selector) != 1 {
			return errors.New("type needs exactly one of ref or selector")
		}
		if a.Text == "" {
			return errors.New("type needs text")
		}
	case OpRead:
		if a.Mode != ReadModeText && a.Mode != ReadModeOutline {
			return fmt.Errorf("read mode must be %q or %q", ReadModeText, ReadModeOutline)
		}
	}
	if len(a.Text) > MaxTypeTextBytes {
		return fmt.Errorf("text exceeds %d bytes", MaxTypeTextBytes)
	}
	return nil
}

// fieldsOutside lists set fields that op does not accept.
func (a BrowserArgs) fieldsOutside(op Op) []string {
	allowed := map[Op]string{
		OpOpen:  "url path tab_id new_tab profile",
		OpTabs:  "",
		OpClose: "tab_id",
		OpClick: "tab_id ref selector text",
		OpType:  "tab_id ref selector text submit",
		OpRead:  "tab_id mode",
		OpShot:  "tab_id full_page",
	}
	accepts := strings.Fields(allowed[op])
	set := []struct {
		name string
		on   bool
	}{
		{"url", a.URL != ""}, {"path", a.Path != ""}, {"tab_id", a.TabID != ""}, {"new_tab", a.NewTab},
		{"profile", a.Profile != ""}, {"ref", a.Ref != ""}, {"selector", a.Selector != ""}, {"text", a.Text != ""},
		{"submit", a.Submit}, {"mode", a.Mode != ""}, {"full_page", a.FullPage},
	}
	var extra []string
	for _, field := range set {
		if field.on && !slices.Contains(accepts, field.name) {
			extra = append(extra, field.name)
		}
	}
	return extra
}

func countSet(values ...string) int {
	n := 0
	for _, v := range values {
		if v != "" {
			n++
		}
	}
	return n
}

// validatePagePath applies the app's file page rule: an absolute path
// without ".." elements or control characters.
func validatePagePath(path string) error {
	if strings.ContainsAny(path, "\x00\r\n") || !filepath.IsAbs(path) {
		return errors.New("path must be absolute")
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return errors.New("path must not contain ..")
		}
	}
	return nil
}

// DecodeRead parses a read result and enforces the text cap.
func DecodeRead(raw json.RawMessage) (ReadResult, error) {
	var result ReadResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("read result: %w", err)
	}
	if len(result.Text) > MaxReadTextBytes {
		return result, fmt.Errorf("read text exceeds %d bytes", MaxReadTextBytes)
	}
	return result, nil
}

// DecodeShot parses a shot result and returns the decoded image, enforcing
// the size cap before and after decoding.
func DecodeShot(raw json.RawMessage) (ShotResult, []byte, error) {
	var result ShotResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, nil, fmt.Errorf("shot result: %w", err)
	}
	if base64.StdEncoding.DecodedLen(len(result.ImageB64)) > MaxShotBytes+2 {
		return result, nil, fmt.Errorf("shot exceeds %d bytes", MaxShotBytes)
	}
	image, err := base64.StdEncoding.DecodeString(result.ImageB64)
	if err != nil {
		return result, nil, fmt.Errorf("shot image: %w", err)
	}
	if len(image) == 0 {
		return result, nil, errors.New("shot image is empty")
	}
	if len(image) > MaxShotBytes {
		return result, nil, fmt.Errorf("shot exceeds %d bytes", MaxShotBytes)
	}
	return result, image, nil
}

// Focus claim lease: an app renews its claim every FocusRenew while it is
// connected; a claim counts until its lease_until.
const (
	FocusLease = 10 * time.Minute
	FocusRenew = 5 * time.Minute
)

// FocusClaim is the data of a macapp.app.focus frame.
type FocusClaim struct {
	AppID      string    `json:"app_id"`
	Mac        string    `json:"mac"`
	User       string    `json:"user"`
	ClaimedAt  time.Time `json:"claimed_at"`
	LeaseUntil time.Time `json:"lease_until"`
}

// Valid reports whether the claim names an app and its lease covers now.
func (c FocusClaim) Valid(now time.Time) bool {
	return strings.TrimSpace(c.AppID) != "" && !c.ClaimedAt.IsZero() && c.LeaseUntil.After(c.ClaimedAt) && now.Before(c.LeaseUntil)
}

// ActiveClaim returns the newest valid claim (last focused wins). Claims are
// in log order, so on equal claimed_at the later one wins.
func ActiveClaim(claims []FocusClaim, now time.Time) (FocusClaim, bool) {
	var active FocusClaim
	found := false
	for _, claim := range claims {
		if !claim.Valid(now) {
			continue
		}
		if !found || !claim.ClaimedAt.Before(active.ClaimedAt) {
			active, found = claim, true
		}
	}
	return active, found
}
