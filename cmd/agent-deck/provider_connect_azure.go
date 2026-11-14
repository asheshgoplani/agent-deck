package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/term"

	azurevm "github.com/asheshgoplani/agent-deck/examples/providers/azure-vm"
	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Seams the tests swap.
var (
	azConnectSleep        = time.Sleep           // retry backoff
	azConnectLookPath     = exec.LookPath        // finding az
	azConnectGOOS         = runtime.GOOS         // install hint
	azConnectCheckWindow  = 3 * time.Minute      // SP propagation retry window for the check
	azConnectLoginTimeout = 15 * time.Minute     // device-code wait
	azConnectRetryEvery   = 10 * time.Second     // pause between check tries
	azConnectStderrMax    = 2000                 // runes of az stderr shown on failure
	azConnectSecretMax    = int64(1 << 16)       // bytes read for --sp-secret-stdin
	azConnectProviderDir  = "providers/azure-vm" // under agentpaths.DataDir()
	azConnectSkuTimeout   = 4 * time.Minute      // az vm list-skus can take minutes
)

const (
	azConfFileName   = "azure-vm.conf"
	azSecretFileName = "azure-sp-secret"
)

// azManagedKeys are the azure-vm.conf keys connect always sets, in file
// order. vm_size is managed too, but only written when absent.
var azManagedKeys = []string{"resource_group", "location", "subscription", "sp_app_id", "sp_tenant", "sp_secret_file"}

// Managed keys written only in some states: sp_created = true when connect
// created the service principal (disconnect deletes only those), and
// sp_secret_pending = true while a new secret is being stored (a run that
// sees it does not trust the stored secret).
const (
	azKeySPCreated     = "sp_created"
	azKeySecretPending = "sp_secret_pending"
)

// azProbeNSG is the free network security group connect creates and deletes
// to learn whether a region takes new resources for the subscription.
const azProbeNSG = "agent-deck-region-probe"

// azPreferredSizes are the small sizes connect may pick, in order, when
// neither --vm-size nor vm_size names one.
var azPreferredSizes = []string{"Standard_B2s", "Standard_B2als_v2", "Standard_B2as_v2", "Standard_B2s_v2", "Standard_D2as_v5", "Standard_D2s_v5"}

// azFallbackRegions are tried in order when the default region refuses new
// resources or offers none of azPreferredSizes.
var azFallbackRegions = []string{"swedencentral", "northeurope", "germanywestcentral", "francecentral", "uksouth", "eastus2"}

// azSku is one entry of `az vm list-skus`.
type azSku struct {
	Name         string   `json:"name"`
	ResourceType string   `json:"resourceType"`
	Locations    []string `json:"locations"`
	Restrictions []struct {
		Type            string   `json:"type"`
		ReasonCode      string   `json:"reasonCode"`
		Values          []string `json:"values"`
		RestrictionInfo struct {
			Locations []string `json:"locations"`
			Zones     []string `json:"zones"`
		} `json:"restrictionInfo"`
	} `json:"restrictions"`
}

// azSkuOffered reports whether skus offer VM size in loc to this
// subscription.
// A size is offered when the listing has it as a VM size for loc and no
// Location restriction covers loc. Zone restrictions are ignored: sandboxes
// do not pin a zone. Without --all az already leaves out restricted sizes.
func azSkuOffered(skus []azSku, size, loc string) bool {
	for _, s := range skus {
		if !strings.EqualFold(s.Name, size) || !strings.EqualFold(s.ResourceType, "virtualMachines") {
			continue
		}
		if !azContainsFold(s.Locations, loc) {
			continue
		}
		restricted := false
		for _, r := range s.Restrictions {
			if strings.EqualFold(r.Type, "Location") &&
				(azContainsFold(r.Values, loc) || azContainsFold(r.RestrictionInfo.Locations, loc)) {
				restricted = true
			}
		}
		if !restricted {
			return true
		}
	}
	return false
}

func azContainsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// azSizePrefix is the --size filter for size's family: Standard_B2s and
// Standard_B2als_v2 -> Standard_B2 (az matches a substring of the name).
func azSizePrefix(size string) string {
	if m := azSizePrefixRe.FindString(size); m != "" {
		return m
	}
	return size
}

// azRequiredProviders are the resource providers a VM needs; a fresh
// subscription has them unregistered and a service principal cannot
// register them.
var azRequiredProviders = []string{"Microsoft.Compute", "Microsoft.Network"}

var (
	azDeviceURLRe   = regexp.MustCompile(`(https://\S+)`)
	azDeviceCodeRe  = regexp.MustCompile(`code ([A-Z0-9-]{6,})`)
	azWrongTenantRe = regexp.MustCompile(`AADSTS(50020|90072|50034|700016)`)
	azGUIDRe        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	azGroupRe       = regexp.MustCompile(`^[A-Za-z0-9_()-][A-Za-z0-9._()-]{0,89}$`)
	azTenantRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`) // id or domain
	azLocationRe    = regexp.MustCompile(`^[A-Za-z0-9]{1,40}$`)
	azSizeRe        = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	azANSIRe        = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	azVMSizeRe      = regexp.MustCompile(`^(?i)standard_([a-z]+)(\d+)([a-z]*)(?:_(v\d+))?$`)
	azSizePrefixRe  = regexp.MustCompile(`^(?i)standard_[a-z]+\d+`)
)

// azVMSizeVCPUs is the vCPU count of the sizes people pick for a sandbox;
// any other size counts as 2.
var azVMSizeVCPUs = map[string]int{
	"b1s": 1, "b1ms": 1, "b2s": 2, "b2ms": 2, "b2als_v2": 2, "b2as_v2": 2,
	"b2ats_v2": 2, "d2s_v5": 2, "d2as_v5": 2, "b4ms": 4,
}

// azVMSizeFamily names the quota family of the common sizes; others are
// derived (see azQuotaFamily).
var azVMSizeFamily = map[string]string{
	"b1s": "standardBSFamily", "b1ms": "standardBSFamily", "b2s": "standardBSFamily",
	"b2ms": "standardBSFamily", "b4ms": "standardBSFamily",
	"b2als_v2": "standardBasv2Family", "b2as_v2": "standardBasv2Family", "b2ats_v2": "standardBasv2Family",
	"d2s_v5": "standardDSv5Family", "d2as_v5": "standardDASv5Family",
}

func azVMSizeKey(size string) string {
	return strings.TrimPrefix(strings.ToLower(size), "standard_")
}

// vmSizeVCPUs is the vCPU count of size: the table, else the number in the
// name (Standard_D4s_v3 -> 4), else 2.
func vmSizeVCPUs(size string) int {
	if n, ok := azVMSizeVCPUs[azVMSizeKey(size)]; ok {
		return n
	}
	if m := azVMSizeRe.FindStringSubmatch(size); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil && n > 0 && n <= 1024 {
			return n
		}
	}
	return 2
}

// azQuotaFamily returns the list-usage name of size's family:
// Standard_B2s -> standardBSFamily, Standard_D4s_v3 -> standardDSv3Family,
// Standard_E4as_v5 -> standardEASv5Family. "" when the name has another
// shape (then only the regional total is checked).
func azQuotaFamily(size string) string {
	if f, ok := azVMSizeFamily[azVMSizeKey(size)]; ok {
		return f
	}
	m := azVMSizeRe.FindStringSubmatch(size)
	if m == nil {
		return ""
	}
	series, extra, ver := strings.ToLower(m[1]), strings.ToLower(m[3]), m[4]
	if series == "b" {
		// B-series v1 is one family; v2 splits by CPU vendor.
		switch {
		case ver == "":
			return "standardBSFamily"
		case strings.Contains(extra, "a"):
			return "standardBas" + ver + "Family"
		case strings.Contains(extra, "p"):
			return "standardBps" + ver + "Family"
		default:
			return "standardBs" + ver + "Family"
		}
	}
	return "standard" + strings.ToUpper(series+extra) + ver + "Family"
}

// azConnectError is a failure with its JSON kind, its exact user text and
// the exit code.
type azConnectError struct {
	kind string
	msg  string
	code int
}

func (e *azConnectError) Error() string { return e.msg }

func azFail(kind, msg string) error { return &azConnectError{kind: kind, msg: msg, code: 1} }

func azFailCode(kind, msg string, code int) error {
	return &azConnectError{kind: kind, msg: msg, code: code}
}

// azSanitize strips terminal escapes and control characters (newlines and
// tabs stay) and cuts to azConnectStderrMax runes.
func azSanitize(s string) string {
	s = azANSIRe.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > azConnectStderrMax {
		s = string(r[:azConnectStderrMax]) + "..."
	}
	return s
}

// azSaid is what Azure said about a failed call: its stderr, else the error.
func azSaid(stderr string, err error) string {
	if s := azSanitize(stderr); s != "" {
		return s
	}
	if err != nil {
		return azSanitize(err.Error())
	}
	return "(no message)"
}

func azContainsAny(s string, needles ...string) bool {
	low := strings.ToLower(s)
	for _, n := range needles {
		if strings.Contains(low, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func azIsPermissionError(stderr string) bool {
	return azContainsAny(stderr, "AuthorizationFailed", "Authorization_RequestDenied", "Insufficient privileges",
		"Directory.ReadWrite", "does not have authorization to perform action")
}

func azIsNotFound(stderr string) bool {
	return azContainsAny(stderr, "not found", "does not exist", "could not be found", "Request_ResourceNotFound",
		"ResourceNotFound", "No matched assignments")
}

// azConnectOptions are the parsed flags.
type azConnectOptions struct {
	subscription    string
	tenant          string
	resourceGroup   string
	location        string
	locationSet     bool
	vmSize          string
	vmSizeSet       bool
	spName          string
	resetSecret     bool
	spAppID         string
	spSecretStdin   bool
	providerCommand string
	name            string
	disconnect      bool
	yes             bool
	force           bool
	jsonOut         bool
}

// azConnect is one run of connect or disconnect.
type azConnect struct {
	opts       azConnectOptions
	ctx        context.Context
	out        io.Writer
	errOut     io.Writer
	in         *bufio.Reader
	azPath     string
	cfgDir     string
	confPath   string
	secretPath string
	bundleDir  string
	changed    bool               // something in Azure or on disk changed
	vmSize     string             // the size azure-vm.conf gets
	vmSizeSet  bool               // write vmSize even when vm_size is there (asked for or picked)
	skus       map[string][]azSku // list-skus answers by location|prefix
	skusFailed map[string]bool    // locations whose listing failed (warned once)
}

// azSubscription is one entry of `az account list --all`.
type azSubscription struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	State               string `json:"state"`
	TenantID            string `json:"tenantId"`
	TenantDefaultDomain string `json:"tenantDefaultDomain"`
	HomeTenantID        string `json:"homeTenantId"`
	IsDefault           bool   `json:"isDefault"`
	User                struct {
		Name string `json:"name"`
	} `json:"user"`
}

// azSP is the service principal connect ends up with. secret is set only
// when a new one must be stored; it lives in memory until written.
type azSP struct {
	appID   string
	tenant  string
	secret  string
	created bool // create-for-rbac ran in this run
	owned   bool // connect created this app (disconnect may delete it)
	kept    bool // the stored SP and secret are reused
}

func runProviderConnect(profile string, args []string) int {
	_ = profile
	fs := newProviderFlagSet("provider connect", "Usage: agent-deck provider connect azure [options]\n       agent-deck provider connect azure --disconnect [--yes] [--force]")
	var o azConnectOptions
	fs.StringVar(&o.subscription, "subscription", "", "Subscription id or exact name (default: pick)")
	fs.StringVar(&o.tenant, "tenant", "", "Tenant (directory) id or domain to log in to")
	fs.StringVar(&o.resourceGroup, "resource-group", "agent-deck-sandboxes", "The resource group sandboxes live in")
	fs.StringVar(&o.location, "location", "westeurope", "Region for a new resource group")
	fs.StringVar(&o.vmSize, "vm-size", "Standard_B2s", "VM size (written to azure-vm.conf only when absent; used for the quota check)")
	fs.StringVar(&o.spName, "sp-name", "agent-deck-sandboxes-sp", "Service principal display name")
	fs.BoolVar(&o.resetSecret, "reset-secret", false, "Issue a new service principal secret even when a working one is stored")
	fs.StringVar(&o.spAppID, "sp-app-id", "", "Use this existing service principal (with --sp-secret-stdin)")
	fs.BoolVar(&o.spSecretStdin, "sp-secret-stdin", false, "Read the --sp-app-id secret from stdin (one line)")
	fs.StringVar(&o.providerCommand, "provider-command", "", "Provider executable to use instead of the bundled copy")
	fs.StringVar(&o.name, "name", "azure", "The [providers.<name>] entry")
	fs.BoolVar(&o.disconnect, "disconnect", false, "Remove the service principal and the local Azure settings")
	fs.BoolVar(&o.yes, "yes", false, "Do not ask before disconnecting")
	fs.BoolVar(&o.force, "force", false, "Disconnect even when sandboxes still exist")
	fs.BoolVar(&o.jsonOut, "json", false, "JSON-lines events on stdout")
	if err := parseCLIFlags(fs, reorderRemoteArgs(fs, args)); err != nil {
		return 2
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "location":
			o.locationSet = true
		case "vm-size":
			o.vmSizeSet = true
		}
	})

	c := &azConnect{opts: o, out: providerDeps.stdout, errOut: providerDeps.stderr, in: bufio.NewReader(providerDeps.stdin)}
	if o.jsonOut {
		c.out = io.Discard
	}
	usage := func(msg string) int {
		c.report(azFailCode(azKindUsage, msg, 2))
		if !o.jsonOut {
			fs.SetOutput(providerDeps.stderr)
			fs.Usage()
		}
		return 2
	}
	if fs.NArg() != 1 {
		return usage("Error: requires exactly one provider to connect (azure)")
	}
	if target := fs.Arg(0); target != "azure" {
		return c.report(azFailCode(azKindUnknownConnectTarget, fmt.Sprintf(azTextUnknownTargetFmt, target), 2))
	}
	if (o.spAppID != "") != o.spSecretStdin {
		return usage("Error: --sp-app-id and --sp-secret-stdin go together")
	}
	if o.spAppID != "" && !azGUIDRe.MatchString(o.spAppID) {
		return usage(fmt.Sprintf("Error: --sp-app-id %q is not an app id (a GUID)", o.spAppID))
	}
	if strings.TrimSpace(o.name) == "" || strings.TrimSpace(o.resourceGroup) == "" || strings.TrimSpace(o.spName) == "" {
		return usage("Error: --name, --resource-group and --sp-name must not be empty")
	}
	for _, v := range []string{o.subscription, o.tenant, o.resourceGroup, o.location, o.vmSize, o.spName} {
		if strings.HasPrefix(v, "-") || strings.HasPrefix(v, "@") || strings.ContainsAny(v, "\n\r\x00") {
			return usage(fmt.Sprintf("Error: invalid value %q", v))
		}
	}
	// Values that reach az argv: Azure's own shapes only.
	for _, f := range []struct {
		flag, v string
		re      *regexp.Regexp
	}{
		{"--resource-group", o.resourceGroup, azGroupRe},
		{"--tenant", o.tenant, azTenantRe},
		{"--location", o.location, azLocationRe},
		{"--vm-size", o.vmSize, azSizeRe},
	} {
		if f.v != "" && !f.re.MatchString(f.v) {
			return usage(fmt.Sprintf("Error: invalid %s value %q", f.flag, f.v))
		}
	}
	if o.disconnect && (o.spAppID != "" || o.resetSecret) {
		return usage("Error: --disconnect does not take --sp-app-id or --reset-secret")
	}
	// The bundled provider is a bash script; on Windows it cannot run.
	if !o.disconnect && azConnectGOOS == "windows" && strings.TrimSpace(o.providerCommand) == "" {
		return c.report(azFail(azKindUnsupportedOS, azTextWindowsUseWSL))
	}

	ctx, stop := providerSignalContext()
	defer stop()
	c.ctx = ctx
	if err := c.initPaths(); err != nil {
		return c.report(azFail(azKindStoreFailed, fmt.Sprintf("Error: %v", err)))
	}
	var err error
	if o.disconnect {
		err = c.disconnect()
	} else {
		err = c.connect()
	}
	if err != nil {
		return c.report(err)
	}
	return 0
}

// report prints err (an error event with --json) and returns its exit code.
func (c *azConnect) report(err error) int {
	var ce *azConnectError
	if !errors.As(err, &ce) {
		ce = &azConnectError{kind: azKindAzFailed, msg: fmt.Sprintf("Error: %v", err), code: 1}
	}
	if c.opts.jsonOut {
		c.emit(map[string]any{"event": "error", "kind": ce.kind, "message": ce.msg})
	} else {
		fmt.Fprintln(c.errOut, ce.msg)
	}
	return ce.code
}

// emit writes one JSON event line on stdout.
func (c *azConnect) emit(ev map[string]any) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintln(providerDeps.stdout, string(data))
}

// step reports progress: a JSON step event, or msg as a human line (warn
// lines on stderr).
func (c *azConnect) step(step, status, msg string) {
	if c.opts.jsonOut {
		c.emit(map[string]any{"event": "step", "step": step, "status": status, "message": msg})
		return
	}
	if msg == "" {
		return
	}
	if status == "warn" {
		fmt.Fprintln(c.errOut, msg)
		return
	}
	fmt.Fprintln(c.out, msg)
}

func (c *azConnect) initPaths() error {
	dir, err := agentpaths.ConfigDir()
	if err != nil {
		return fmt.Errorf("finding the agent-deck config dir: %w", err)
	}
	data, err := agentpaths.DataDir()
	if err != nil {
		return fmt.Errorf("finding the agent-deck data dir: %w", err)
	}
	c.cfgDir = dir
	c.confPath = filepath.Join(dir, azConfFileName)
	// The provider reads AGENTDECK_AZURE_CONFIG first; write where it reads.
	if p := strings.TrimSpace(os.Getenv("AGENTDECK_AZURE_CONFIG")); p != "" && filepath.IsAbs(p) {
		c.confPath = filepath.Clean(p)
	}
	c.secretPath = filepath.Join(dir, azSecretFileName)
	c.bundleDir = filepath.Join(data, filepath.FromSlash(azConnectProviderDir))
	return nil
}

func (c *azConnect) bundledCommand() string { return filepath.Join(c.bundleDir, "provider") }

// ---- az ----

// az runs one az call with --only-show-errors and -o format, never through
// a shell, with the inherited env and no stdin.
func (c *azConnect) az(format string, args ...string) ([]byte, string, error) {
	return c.azCtx(c.ctx, format, args...)
}

func (c *azConnect) azCtx(ctx context.Context, format string, args ...string) ([]byte, string, error) {
	full := append(append([]string{}, args...), "--only-show-errors", "-o", format)
	// #nosec G204 -- az comes from LookPath; every argument is fixed or a validated flag value, never a secret
	cmd := exec.CommandContext(ctx, c.azPath, full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return stdout.Bytes(), stderr.String(), err
}

func azCallFailed(what, stderr string, err error) error {
	return azFail(azKindAzFailed, fmt.Sprintf(azTextAzFailedFmt, what, azSaid(stderr, err)))
}

func (c *azConnect) findAz() error {
	c.step("az", "start", "")
	p, err := azConnectLookPath("az")
	if err != nil {
		return azFail(azKindAzMissing, azTextAzMissing(azConnectGOOS))
	}
	c.azPath = p
	c.step("az", "ok", "")
	return nil
}

// azLogin is the signed-in account of `az account show`.
type azLogin struct {
	user   string
	tenant string // the directory az's current login is for
}

// account reads `az account show`; ok=false when az is not logged in.
func (c *azConnect) account() (azLogin, bool) {
	out, _, err := c.az("json", "account", "show")
	if err != nil {
		return azLogin{}, false
	}
	var acct struct {
		TenantID string `json:"tenantId"`
		User     struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	_ = json.Unmarshal(out, &acct)
	return azLogin{user: acct.User.Name, tenant: acct.TenantID}, true
}

// ensureLogin reuses the user's az login or runs a device-code login.
func (c *azConnect) ensureLogin() (azLogin, error) {
	c.step("login", "start", "")
	if login, ok := c.account(); ok {
		c.step("login", "ok", fmt.Sprintf(azTextUsingLoginFmt, firstNonEmpty(login.user, "(unknown user)")))
		return login, nil
	}
	if !c.opts.jsonOut {
		fmt.Fprintln(c.out, azTextLoginIntro)
	}
	if err := c.deviceLogin(); err != nil {
		return azLogin{}, err
	}
	login, _ := c.account()
	c.step("login", "ok", fmt.Sprintf(azTextUsingLoginFmt, firstNonEmpty(login.user, "(unknown user)")))
	return login, nil
}

// azLineWriter calls line for each complete line written to it.
type azLineWriter struct {
	buf  []byte
	line func(string)
}

func (w *azLineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.line(strings.TrimRight(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

func (w *azLineWriter) flush() {
	if len(w.buf) > 0 {
		w.line(string(w.buf))
		w.buf = nil
	}
}

func (c *azConnect) deviceLogin() error {
	ctx, cancel := context.WithTimeout(c.ctx, azConnectLoginTimeout)
	defer cancel()
	args := []string{"login", "--use-device-code", "--allow-no-subscriptions"}
	if c.opts.tenant != "" {
		args = append(args, "--tenant", c.opts.tenant)
	}
	args = append(args, "--only-show-errors", "-o", "none")
	// #nosec G204 -- az comes from LookPath; the arguments are fixed or a validated --tenant
	cmd := exec.CommandContext(ctx, c.azPath, args...)
	var kept []string
	shown := false
	lw := &azLineWriter{line: func(line string) {
		if !shown {
			u, code := azDeviceURLRe.FindStringSubmatch(line), azDeviceCodeRe.FindStringSubmatch(line)
			if u != nil && code != nil {
				shown = true
				url := strings.TrimRight(u[1], ".,;)")
				if c.opts.jsonOut {
					c.emit(map[string]any{"event": "device_code", "url": url, "code": code[1]})
				} else {
					fmt.Fprintln(c.out, azTextDeviceCode(url, code[1]))
				}
				return
			}
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}}
	cmd.Stdout = io.Discard
	cmd.Stderr = lw
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	lw.flush()
	if err == nil {
		return nil
	}
	said := strings.Join(kept, "\n")
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return azFail(azKindLoginFailed, azTextLoginFailed(azTextLoginTimedOut))
	case azWrongTenantRe.MatchString(said):
		code := "AADSTS" + azWrongTenantRe.FindStringSubmatch(said)[1]
		return azFail(azKindWrongTenant, azTextWrongTenantLogin(firstNonEmpty(c.opts.tenant, "(your default directory)"), code))
	case azContainsAny(said, "No subscriptions found"):
		return azFail(azKindNoSubscription, azTextNoSubscription("your account"))
	}
	return azFail(azKindLoginFailed, azTextLoginFailed(azSaid(said, err)))
}

// ---- subscription ----

func azSubLine(s azSubscription) string { return fmt.Sprintf("  %s (%s)", s.Name, s.ID) }

func azSubList(subs []azSubscription, withState bool) string {
	lines := make([]string, 0, len(subs))
	for _, s := range subs {
		l := azSubLine(s)
		if withState {
			l += ": " + firstNonEmpty(s.State, "unknown state")
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

func (s azSubscription) inTenant(t string) bool {
	return strings.EqualFold(s.TenantID, t) || (s.TenantDefaultDomain != "" && strings.EqualFold(s.TenantDefaultDomain, t)) ||
		(s.HomeTenantID != "" && strings.EqualFold(s.HomeTenantID, t))
}

func (s azSubscription) usable() bool { return s.State == "Enabled" }

func (c *azConnect) pickSubscription(user string) (azSubscription, error) {
	c.step("subscription", "start", "")
	out, stderr, err := c.az("json", "account", "list", "--all")
	if err != nil {
		return azSubscription{}, azCallFailed("account list", stderr, err)
	}
	var raw []azSubscription
	if err := json.Unmarshal(out, &raw); err != nil {
		return azSubscription{}, azFail(azKindAzFailed, fmt.Sprintf("Error: reading az account list: %v", err))
	}
	var all []azSubscription
	for _, s := range raw {
		if user == "" && s.User.Name != "" {
			user = s.User.Name
		}
		if s.Name == "N/A(tenant level account)" || strings.EqualFold(s.ID, s.TenantID) {
			continue
		}
		all = append(all, s)
	}
	inTenant, elsewhere := all, []azSubscription(nil)
	if t := c.opts.tenant; t != "" {
		inTenant = nil
		for _, s := range all {
			if s.inTenant(t) {
				inTenant = append(inTenant, s)
			} else {
				elsewhere = append(elsewhere, s)
			}
		}
	}
	var usable []azSubscription
	for _, s := range inTenant {
		if s.usable() {
			usable = append(usable, s)
		}
	}
	if len(usable) == 0 {
		var otherTenants []string
		seen := map[string]bool{}
		for _, s := range elsewhere {
			t := firstNonEmpty(s.TenantDefaultDomain, s.TenantID)
			if s.usable() && !seen[t] {
				seen[t] = true
				otherTenants = append(otherTenants, t)
			}
		}
		switch {
		case len(otherTenants) > 0:
			return azSubscription{}, azFail(azKindWrongTenant, azTextWrongTenant(c.opts.tenant, strings.Join(otherTenants, ", ")))
		case len(inTenant) > 0:
			return azSubscription{}, azFail(azKindSubscriptionDisabled, azTextSubscriptionDisabled(azSubList(inTenant, true)))
		default:
			return azSubscription{}, azFail(azKindNoSubscription, azTextNoSubscription(firstNonEmpty(user, "your account")))
		}
	}

	var chosen *azSubscription
	switch want := c.opts.subscription; {
	case want != "":
		for i := range inTenant {
			if strings.EqualFold(inTenant[i].ID, want) || inTenant[i].Name == want {
				chosen = &inTenant[i]
				break
			}
		}
		if chosen == nil {
			return azSubscription{}, azFail(azKindSubscriptionNotFound, azTextSubscriptionNotFound(want, azSubList(usable, false)))
		}
		if !chosen.usable() {
			return azSubscription{}, azFail(azKindSubscriptionDisabled, azTextSubscriptionDisabled(azSubList([]azSubscription{*chosen}, true)))
		}
	case len(usable) == 1:
		chosen = &usable[0]
	default:
		if c.opts.jsonOut {
			list := make([]map[string]any, 0, len(usable))
			for _, s := range usable {
				list = append(list, map[string]any{"id": s.ID, "name": s.Name, "tenant_id": s.TenantID, "state": s.State, "default": s.IsDefault})
			}
			c.emit(map[string]any{"event": "subscriptions", "subscriptions": list})
		}
		if c.opts.jsonOut || !providerDeps.isTTY() {
			return azSubscription{}, azFailCode(azKindChooseSubscription, azTextChooseSubscription(azSubList(usable, false)), 3)
		}
		fmt.Fprintln(c.errOut, azTextSubscriptionMenuIntro)
		for i, s := range usable {
			fmt.Fprintf(c.errOut, "  %d) %s (%s)\n", i+1, s.Name, s.ID)
		}
		fmt.Fprintf(c.errOut, azTextSubscriptionMenuAsk, len(usable))
		answer, _ := c.in.ReadString('\n')
		n, err := strconv.Atoi(strings.TrimSpace(answer))
		if err != nil || n < 1 || n > len(usable) {
			return azSubscription{}, azFail(azKindAborted, azTextSubscriptionMenuBad)
		}
		chosen = &usable[n-1]
	}
	// Both go into az argv and azure-vm.conf.
	if !azGUIDRe.MatchString(chosen.ID) {
		return azSubscription{}, azFail(azKindAzFailed, fmt.Sprintf(azTextAzUnexpectedFmt, "account list", "subscription id", chosen.ID))
	}
	if !azGUIDRe.MatchString(chosen.TenantID) {
		return azSubscription{}, azFail(azKindAzFailed, fmt.Sprintf(azTextAzUnexpectedFmt, "account list", "tenant id", chosen.TenantID))
	}
	c.step("subscription", "ok", fmt.Sprintf(azTextSubscriptionFmt, chosen.Name, chosen.ID))
	return *chosen, nil
}

// ---- resource providers, group, quota ----

func (c *azConnect) registerProviders(sub azSubscription) error {
	c.step("providers", "start", "")
	for _, ns := range azRequiredProviders {
		out, stderr, err := c.az("tsv", "provider", "show", "--namespace", ns, "--subscription", sub.ID, "--query", "registrationState")
		if err != nil {
			if azIsPermissionError(stderr) {
				return azFail(azKindNoPermissionRegister, azTextNoPermissionRegister(ns, sub.ID))
			}
			return azCallFailed("provider show --namespace "+ns, stderr, err)
		}
		if strings.TrimSpace(string(out)) == "Registered" {
			continue
		}
		if !c.opts.jsonOut {
			fmt.Fprintln(c.out, fmt.Sprintf(azTextRegisteringFmt, ns))
		}
		_, stderr, err = c.az("none", "provider", "register", "--namespace", ns, "--subscription", sub.ID, "--wait")
		if err != nil {
			if azIsPermissionError(stderr) {
				return azFail(azKindNoPermissionRegister, azTextNoPermissionRegister(ns, sub.ID))
			}
			return azCallFailed("provider register --namespace "+ns, stderr, err)
		}
		c.changed = true
		c.step("providers", "ok", fmt.Sprintf(azTextRegisteredFmt, ns))
	}
	c.step("providers", "ok", "")
	return nil
}

// showGroup returns the existing group's location, or exists=false.
func (c *azConnect) showGroup(sub azSubscription) (loc string, exists bool, err error) {
	rg := c.opts.resourceGroup
	c.step("group", "start", "")
	out, stderr, err := c.az("json", "group", "show", "--name", rg, "--subscription", sub.ID)
	if err == nil {
		var g struct {
			Location string `json:"location"`
		}
		if err := json.Unmarshal(out, &g); err != nil || g.Location == "" {
			return "", false, azFail(azKindAzFailed, fmt.Sprintf("Error: reading az group show: no location for %s", rg))
		}
		if !azLocationRe.MatchString(g.Location) {
			return "", false, azFail(azKindAzFailed, fmt.Sprintf(azTextAzUnexpectedFmt, "group show", "location", g.Location))
		}
		c.step("group", "ok", fmt.Sprintf(azTextGroupExistsFmt, rg, g.Location))
		if c.opts.locationSet && !strings.EqualFold(c.opts.location, g.Location) {
			c.step("group", "warn", fmt.Sprintf(azTextGroupLocationNoteFmt, c.opts.location, g.Location))
		}
		return g.Location, true, nil
	}
	if !azContainsAny(stderr, "ResourceGroupNotFound", "could not be found") {
		if azIsPermissionError(stderr) {
			return "", false, azFail(azKindNoPermissionGroup, azTextNoPermissionGroup(rg, sub.Name, azSaid(stderr, err), c.opts.location, sub.ID))
		}
		return "", false, azCallFailed("group show --name "+rg, stderr, err)
	}
	return "", false, nil
}

// createGroup creates the group in loc and returns its location.
func (c *azConnect) createGroup(sub azSubscription, loc string) (string, error) {
	rg := c.opts.resourceGroup
	out, stderr, err := c.az("json", "group", "create", "--name", rg, "--location", loc, "--subscription", sub.ID, "--tags", "agent-deck=sandboxes")
	if err != nil {
		switch {
		case azIsPermissionError(stderr):
			return "", azFail(azKindNoPermissionGroup, azTextNoPermissionGroup(rg, sub.Name, azSaid(stderr, err), loc, sub.ID))
		case azContainsAny(stderr, "LocationNotAvailableForResourceGroup", "InvalidLocation"):
			return "", azFail(azKindBadLocation, azTextBadLocation(loc))
		}
		return "", azCallFailed("group create --name "+rg, stderr, err)
	}
	var g struct {
		Location string `json:"location"`
	}
	if json.Unmarshal(out, &g) == nil && azLocationRe.MatchString(g.Location) {
		loc = g.Location
	}
	c.changed = true
	c.step("group", "ok", fmt.Sprintf(azTextGroupCreatedFmt, rg, loc))
	return loc, nil
}

// azNum is a list-usage number, which az prints as a number or a string.
type azNum int64

func (n *azNum) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("parsing quota number %q: %w", s, err)
	}
	*n = azNum(f)
	return nil
}

type azUsage struct {
	Name struct {
		Value          string `json:"value"`
		LocalizedValue string `json:"localizedValue"`
	} `json:"name"`
	CurrentValue azNum `json:"currentValue"`
	Limit        azNum `json:"limit"`
}

func (c *azConnect) checkQuota(sub azSubscription, loc, size string) error {
	c.step("quota", "start", "")
	out, stderr, err := c.az("json", "vm", "list-usage", "--location", loc, "--subscription", sub.ID)
	// The quota runs before a new group is made, so it is the first call
	// that sees a region Azure does not have.
	if err != nil && azContainsAny(stderr, "NoRegisteredProviderFound", "InvalidLocation", "LocationNotAvailable") {
		return azFail(azKindBadLocation, azTextBadLocation(loc))
	}
	var usages []azUsage
	if err == nil {
		err = json.Unmarshal(out, &usages)
	}
	if err != nil {
		c.step("quota", "warn", fmt.Sprintf(azTextQuotaUnreadableFmt, loc))
		return nil
	}
	need := vmSizeVCPUs(size)
	family := azQuotaFamily(size)
	var checks []azUsage
	for _, u := range usages {
		if family != "" && strings.EqualFold(u.Name.Value, family) {
			checks = append([]azUsage{u}, checks...)
		} else if u.Name.Value == "cores" {
			checks = append(checks, u)
		}
	}
	minFree := -1
	for _, u := range checks {
		free := int(u.Limit) - int(u.CurrentValue)
		if free < need {
			name := firstNonEmpty(u.Name.LocalizedValue, u.Name.Value)
			return azFail(azKindQuota, azTextQuota(loc, size, name, int(u.Limit), int(u.CurrentValue), need))
		}
		if minFree < 0 || free < minFree {
			minFree = free
		}
	}
	if minFree < 0 {
		c.step("quota", "warn", fmt.Sprintf(azTextQuotaUnreadableFmt, loc))
		return nil
	}
	c.step("quota", "ok", fmt.Sprintf(azTextQuotaOKFmt, loc, minFree, size, need))
	return nil
}

// ---- region and size ----

// azFirstLine is the first non-empty line of s.
func azFirstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return s
}

// regionCandidate is the region to try first for the VMs, and whether it
// was asked for (--location for a new group, or the group's own region).
// A stored location (maybe a fallback of an earlier run) wins over the
// group's region.
func (c *azConnect) regionCandidate(conf *azConfFile, sub azSubscription, groupLoc string, groupExists bool) (string, bool) {
	if c.opts.locationSet && (!groupExists || strings.EqualFold(c.opts.location, groupLoc)) {
		return c.opts.location, true
	}
	if v := conf.get("location"); v != "" && groupExists && conf.get("resource_group") == c.opts.resourceGroup &&
		strings.EqualFold(conf.get("subscription"), sub.ID) {
		return v, false
	}
	if groupExists {
		return groupLoc, false
	}
	return c.opts.location, false
}

// keepStoredSetup reports whether this run keeps a working stored setup
// unchanged; then the region and size are not tested again.
func (c *azConnect) keepStoredSetup(conf *azConfFile, sub azSubscription, groupExists bool, loc string) bool {
	confLoc, confSize := conf.get("location"), conf.get("vm_size")
	return groupExists && c.spKeepable(conf, sub) && confLoc != "" && confSize != "" &&
		strings.EqualFold(loc, confLoc) && (!c.opts.vmSizeSet || strings.EqualFold(c.opts.vmSize, confSize))
}

// probeRegion creates and deletes a free network security group in loc, as
// the signed-in user; closed=true when Azure refuses new resources there
// (RequestDisallowedByAzure). Any other failure warns and counts as open.
func (c *azConnect) probeRegion(sub azSubscription, loc string) (closed bool) {
	rg := c.opts.resourceGroup
	_, stderr, err := c.az("none", "network", "nsg", "create", "--name", azProbeNSG, "--resource-group", rg,
		"--location", loc, "--tags", "agent-deck=probe", "--subscription", sub.ID)
	if err != nil {
		if azContainsAny(stderr, "RequestDisallowedByAzure", "not accepting new customers") {
			return true
		}
		c.step("region", "warn", fmt.Sprintf(azTextRegionProbeFailedFmt, loc, azFirstLine(azSaid(stderr, err))))
		return false
	}
	if _, _, err := c.az("none", "network", "nsg", "delete", "--name", azProbeNSG, "--resource-group", rg, "--subscription", sub.ID); err != nil {
		c.step("region", "warn", fmt.Sprintf(azTextProbeDeleteFailedFmt, azProbeNSG, rg, azProbeNSG, rg, sub.ID))
	}
	return false
}

// sizeOffered reports whether loc offers size to this subscription.
// known=false when the listing failed (warned once per region) or is empty;
// then the size is taken on trust.
func (c *azConnect) sizeOffered(sub azSubscription, loc, size string) (offered, known bool) {
	if c.skus == nil {
		c.skus, c.skusFailed = map[string][]azSku{}, map[string]bool{}
	}
	lloc := strings.ToLower(loc)
	if c.skusFailed[lloc] {
		return false, false
	}
	prefix := azSizePrefix(size)
	key := lloc + "|" + strings.ToLower(prefix)
	skus, ok := c.skus[key]
	if !ok {
		ctx, cancel := context.WithTimeout(c.ctx, azConnectSkuTimeout)
		out, stderr, err := c.azCtx(ctx, "json", "vm", "list-skus", "--location", loc, "--size", prefix,
			"--resource-type", "virtualMachines", "--all", "--subscription", sub.ID)
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
		if err == nil {
			err = json.Unmarshal(out, &skus)
		}
		if err != nil {
			c.skusFailed[lloc] = true
			said := azFirstLine(azSaid(stderr, err))
			if timedOut {
				said = fmt.Sprintf(azTextSkusTimedOutFmt, azConnectSkuTimeout)
			}
			c.step("region", "warn", fmt.Sprintf(azTextSkusUnreadableFmt, loc, said, size))
			return false, false
		}
		c.skus[key] = skus
	}
	if len(skus) == 0 {
		return false, false
	}
	return azSkuOffered(skus, size, loc), true
}

// pickSize returns the size to use in loc: size itself when it was asked
// for, else the first offered of size and azPreferredSizes. ok=false when
// none is offered.
func (c *azConnect) pickSize(sub azSubscription, loc, size string, pinned bool) (string, bool) {
	cands := []string{size}
	if !pinned {
		for _, p := range azPreferredSizes {
			if !strings.EqualFold(p, size) {
				cands = append(cands, p)
			}
		}
	}
	for _, s := range cands {
		if offered, known := c.sizeOffered(sub, loc, s); offered || !known {
			return s, true
		}
	}
	return "", false
}

// offeredSizes lists the azPreferredSizes loc is known to offer.
func (c *azConnect) offeredSizes(sub azSubscription, loc string) []string {
	var out []string
	for _, p := range azPreferredSizes {
		if offered, known := c.sizeOffered(sub, loc, p); offered && known {
			out = append(out, p)
		}
	}
	return out
}

// chooseRegion settles where the VMs go and their size. Order: size check
// (no group needed), quota, the group (made in loc when missing), then the
// probe. A region that refuses new resources, or offers none of the small
// sizes, falls back to azFallbackRegions unless it was asked for. The quota
// is read again when the region or size changed. Returns the VM region,
// the size and the group's region.
func (c *azConnect) chooseRegion(sub azSubscription, loc string, locPinned bool, size string, sizePinned, groupExists bool, groupLoc string) (string, string, string, error) {
	c.step("region", "start", "")
	picked, sizeOK := c.pickSize(sub, loc, size, sizePinned)
	if !sizeOK && (sizePinned || locPinned) {
		return "", "", "", azFail(azKindSizeUnavailable, azTextSizeUnavailable(size, loc, c.offeredSizes(sub, loc), c.confPath))
	}
	quotaDone := ""
	if sizeOK {
		// Quota before creating the group, so a full region leaves nothing behind.
		if err := c.checkQuota(sub, loc, picked); err != nil {
			return "", "", "", err
		}
		quotaDone = loc + "|" + picked
	}
	if !groupExists {
		gl, err := c.createGroup(sub, loc)
		if err != nil {
			return "", "", "", err
		}
		groupLoc = gl
	}
	final, finalSize, note := loc, picked, ""
	switch {
	case c.probeRegion(sub, loc):
		if locPinned {
			var working, tried []string
			for _, r := range azFallbackRegions {
				if len(tried) == 3 {
					break
				}
				if strings.EqualFold(r, loc) {
					continue
				}
				tried = append(tried, r)
				if !c.probeRegion(sub, r) {
					working = append(working, r)
				}
			}
			return "", "", "", azFail(azKindRegionClosed, azTextRegionClosed(loc, working, tried))
		}
		final, note = "", azTextRegionUsingFmt
	case !sizeOK:
		final, note = "", azTextRegionNoSizeUsingFmt
	}
	if final == "" {
		var tried []string
		for _, r := range azFallbackRegions {
			if strings.EqualFold(r, loc) {
				continue
			}
			tried = append(tried, r)
			if c.probeRegion(sub, r) {
				continue
			}
			if s, ok := c.pickSize(sub, r, size, sizePinned); ok {
				final, finalSize = r, s
				break
			}
		}
		if final == "" {
			return "", "", "", azFail(azKindRegionClosed, azTextRegionClosed(loc, nil, tried))
		}
		c.step("region", "warn", fmt.Sprintf(note, loc, final))
	}
	if !strings.EqualFold(finalSize, size) {
		c.step("region", "warn", fmt.Sprintf(azTextSizeUsingFmt, size, final, finalSize, vmSizeVCPUs(finalSize)))
		c.vmSize, c.vmSizeSet = finalSize, true
	}
	if final+"|"+finalSize != quotaDone {
		if err := c.checkQuota(sub, final, finalSize); err != nil {
			return "", "", "", err
		}
	}
	c.step("region", "ok", fmt.Sprintf(azTextRegionOKFmt, final, finalSize))
	return final, finalSize, groupLoc, nil
}

// ---- service principal ----

func (c *azConnect) scope(sub azSubscription) string {
	return "/subscriptions/" + sub.ID + "/resourceGroups/" + c.opts.resourceGroup
}

func (c *azConnect) ensureSP(sub azSubscription, conf *azConfFile) (azSP, error) {
	c.step("sp", "start", "")
	confApp := conf.get("sp_app_id")
	switch {
	case c.opts.spAppID != "":
		secret, err := c.readSecret()
		if err != nil {
			return azSP{}, err
		}
		c.step("sp", "ok", fmt.Sprintf(azTextSPGivenFmt, c.opts.spAppID))
		return azSP{appID: c.opts.spAppID, tenant: sub.TenantID, secret: secret}, nil
	case c.spKeepable(conf, sub):
		c.step("sp", "ok", fmt.Sprintf(azTextSPKeptFmt, confApp))
		return azSP{appID: confApp, tenant: firstNonEmpty(conf.get("sp_tenant"), sub.TenantID),
			owned: conf.get(azKeySPCreated) == "true", kept: true}, nil
	}

	// create-for-rbac on an existing display name patches that app and
	// replaces its secrets, so only reuse a name that is ours, or on purpose.
	existing, err := c.findSPByName(sub)
	if err != nil {
		return azSP{}, err
	}
	ours := existing != "" && strings.EqualFold(existing, confApp)
	if existing != "" && !ours && !c.opts.resetSecret {
		return azSP{}, azFail(azKindSPNameTaken, azTextSPNameTaken(c.opts.spName, existing))
	}
	owned := existing == "" || (ours && conf.get(azKeySPCreated) == "true")

	if !c.opts.jsonOut {
		fmt.Fprintln(c.out, fmt.Sprintf(azTextSPCreatingFmt, c.opts.spName, c.opts.resourceGroup))
	}
	out, stderr, err := c.az("json", "ad", "sp", "create-for-rbac", "--name", c.opts.spName, "--role", "Contributor", "--scopes", c.scope(sub))
	if err != nil {
		if azIsPermissionError(stderr) {
			return azSP{}, azFail(azKindNoPermissionSP, azTextNoPermissionSP(azSaid(stderr, err), sub.Name, sub.ID, c.opts.resourceGroup, c.opts.spName))
		}
		return azSP{}, azCallFailed("ad sp create-for-rbac", stderr, err)
	}
	var created struct {
		AppID    string `json:"appId"`
		Password string `json:"password"`
		Tenant   string `json:"tenant"`
	}
	// Never echo out on failure: it holds the password.
	if err := json.Unmarshal(out, &created); err != nil || created.AppID == "" || created.Password == "" {
		return azSP{}, azFail(azKindAzFailed, "Error: az ad sp create-for-rbac returned no app id or secret")
	}
	if !azGUIDRe.MatchString(created.AppID) {
		return azSP{}, azFail(azKindAzFailed, fmt.Sprintf(azTextAzUnexpectedFmt, "ad sp create-for-rbac", "app id", created.AppID))
	}
	if created.Tenant != "" && !azGUIDRe.MatchString(created.Tenant) {
		return azSP{}, azFail(azKindAzFailed, fmt.Sprintf(azTextAzUnexpectedFmt, "ad sp create-for-rbac", "tenant id", created.Tenant))
	}
	c.changed = true
	c.step("sp", "ok", fmt.Sprintf(azTextSPCreatedFmt, created.AppID))
	return azSP{appID: created.AppID, tenant: firstNonEmpty(created.Tenant, sub.TenantID), secret: created.Password,
		created: true, owned: owned}, nil
}

// spKeepable reports whether the stored service principal and secret are
// reused as they are.
func (c *azConnect) spKeepable(conf *azConfFile, sub azSubscription) bool {
	return c.opts.spAppID == "" && !c.opts.resetSecret && conf.get("sp_app_id") != "" && azRegularFile(c.secretPath) &&
		conf.get(azKeySecretPending) != "true" &&
		strings.EqualFold(conf.get("subscription"), sub.ID) && conf.get("resource_group") == c.opts.resourceGroup
}

// findSPByName returns the appId of the service principal whose display
// name is exactly --sp-name ("" when there is none). az matches by prefix,
// so the exact name is checked here.
func (c *azConnect) findSPByName(sub azSubscription) (string, error) {
	out, stderr, err := c.az("json", "ad", "sp", "list", "--display-name", c.opts.spName)
	if err != nil {
		if azIsPermissionError(stderr) {
			return "", azFail(azKindNoPermissionSP, azTextNoPermissionSP(azSaid(stderr, err), sub.Name, sub.ID, c.opts.resourceGroup, c.opts.spName))
		}
		return "", azCallFailed("ad sp list --display-name "+c.opts.spName, stderr, err)
	}
	var sps []struct {
		AppID       string `json:"appId"`
		DisplayName string `json:"displayName"`
	}
	if err := json.Unmarshal(out, &sps); err != nil {
		return "", azFail(azKindAzFailed, fmt.Sprintf("Error: reading az ad sp list: %v", err))
	}
	for _, sp := range sps {
		if strings.EqualFold(sp.DisplayName, c.opts.spName) && sp.AppID != "" {
			return sp.AppID, nil
		}
	}
	return "", nil
}

// readSecret reads the --sp-secret-stdin secret: exactly one non-empty
// line. It stops at the first newline (or EOF), so a pipe that stays open
// does not block it.
func (c *azConnect) readSecret() (string, error) {
	if f, ok := providerDeps.stdin.(*os.File); ok && providerDeps.isTTY() && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(c.errOut, azTextSecretPrompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(c.errOut)
		if err != nil {
			return "", azFailCode(azKindSecretInvalid, fmt.Sprintf("Error: reading the secret: %v", err), 2)
		}
		return azCheckSecret(string(b))
	}
	var line []byte
	for {
		b, err := c.in.ReadByte()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", azFailCode(azKindSecretInvalid, fmt.Sprintf("Error: reading the secret: %v", err), 2)
		}
		if b == '\n' {
			break
		}
		if int64(len(line)) >= azConnectSecretMax {
			return "", azFailCode(azKindSecretInvalid, azTextSecretTooLong, 2)
		}
		line = append(line, b)
	}
	// More text already buffered after the line: a second line.
	if n := c.in.Buffered(); n > 0 {
		if rest, _ := c.in.Peek(n); strings.TrimSpace(string(rest)) != "" {
			return "", azFailCode(azKindSecretInvalid, azTextSecretMultiLine, 2)
		}
	}
	return azCheckSecret(string(line))
}

func azCheckSecret(s string) (string, error) {
	s = strings.TrimRight(s, "\r\n")
	switch {
	case strings.TrimSpace(s) == "":
		return "", azFailCode(azKindSecretInvalid, azTextSecretEmpty, 2)
	case strings.ContainsAny(s, "\r\n\x00"):
		return "", azFailCode(azKindSecretInvalid, azTextSecretMultiLine, 2)
	}
	return s, nil
}

// checkScope warns when the SP has roles outside the group (not fatal).
func (c *azConnect) checkScope(sub azSubscription, appID string) {
	out, _, err := c.az("json", "role", "assignment", "list", "--assignee", appID, "--all", "--subscription", sub.ID)
	var roles []struct {
		Scope              string `json:"scope"`
		RoleDefinitionName string `json:"roleDefinitionName"`
	}
	if err == nil {
		err = json.Unmarshal(out, &roles)
	}
	if err != nil {
		c.step("sp", "warn", azTextRoleUnreadable)
		return
	}
	want := c.scope(sub)
	var wider []string
	for _, r := range roles {
		if !strings.EqualFold(strings.TrimRight(r.Scope, "/"), want) {
			wider = append(wider, fmt.Sprintf("  %s on %s", firstNonEmpty(r.RoleDefinitionName, "(role)"), r.Scope))
		}
	}
	if len(wider) > 0 {
		c.step("sp", "warn", azTextScopeWider(appID, c.opts.resourceGroup, strings.Join(wider, "\n")))
		return
	}
	c.step("sp", "ok", fmt.Sprintf(azTextRoleCleanFmt, c.opts.resourceGroup))
}

// ---- local files ----

func azRegularFile(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular()
}

// azWriteFileAtomic writes data to p via a temp file in the same dir
// (created 0600), fsync and rename, then sets mode.
func azWriteFileAtomic(p string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(p)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating a temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("setting the mode of %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpName, p, err)
	}
	ok = true
	return nil
}

// azConfFile is azure-vm.conf as lines, so every line connect does not
// manage stays as it is.
type azConfFile struct {
	exists bool
	lines  []string
}

func azReadConf(p string) (*azConfFile, error) {
	data, err := os.ReadFile(p) // #nosec G304 -- agent-deck's own config file
	if errors.Is(err, fs.ErrNotExist) {
		return &azConfFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	f := &azConfFile{exists: true}
	if text != "" {
		f.lines = strings.Split(text, "\n")
	}
	return f, nil
}

// azConfKey returns the key of a "key = value" line ("" for anything else).
func azConfKey(line string) (string, string) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", ""
	}
	k, v, ok := strings.Cut(t, "=")
	if !ok {
		return "", ""
	}
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == v[len(v)-1] && (v[0] == '"' || v[0] == '\'') {
		v = v[1 : len(v)-1]
	}
	return strings.TrimSpace(k), v
}

// get returns the last value of key (the provider's parser keeps the last).
func (f *azConfFile) get(key string) string {
	val := ""
	for _, l := range f.lines {
		if k, v := azConfKey(l); k == key {
			val = v
		}
	}
	return val
}

func (f *azConfFile) has(key string) bool {
	for _, l := range f.lines {
		if k, _ := azConfKey(l); k == key {
			return true
		}
	}
	return false
}

// set updates key in place on its existing line(s), or adds it after the
// managed block (before the marker when there is one, else at the end).
func (f *azConfFile) set(key, value string) {
	line := key + " = " + value
	found := false
	for i, l := range f.lines {
		if k, _ := azConfKey(l); k == key {
			f.lines[i] = line
			found = true
		}
	}
	if found {
		return
	}
	for i, l := range f.lines {
		if strings.TrimSpace(l) == azTextConfFileMarker {
			f.lines = append(f.lines[:i], append([]string{line}, f.lines[i:]...)...)
			return
		}
	}
	f.lines = append(f.lines, line)
}

func (f *azConfFile) remove(keys ...string) {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	kept := f.lines[:0]
	for _, l := range f.lines {
		if k, _ := azConfKey(l); drop[k] {
			continue
		}
		kept = append(kept, l)
	}
	f.lines = kept
}

// onlyOurs reports whether only connect's header, marker and blank lines
// are left.
func (f *azConfFile) onlyOurs() bool {
	for _, l := range f.lines {
		t := strings.TrimSpace(l)
		if t != "" && t != azTextConfFileHeader && t != azTextConfFileMarker {
			return false
		}
	}
	return true
}

func (f *azConfFile) bytes() []byte { return []byte(strings.Join(f.lines, "\n") + "\n") }

// writeConf writes the managed keys into azure-vm.conf; reports whether the
// file changed. pending marks a new secret that is not stored yet.
func (c *azConnect) writeConf(conf *azConfFile, sub azSubscription, loc string, sp azSP, pending bool) (bool, error) {
	before := ""
	if conf.exists {
		before = string(conf.bytes())
	}
	if !conf.exists {
		conf.lines = []string{azTextConfFileHeader}
	}
	values := map[string]string{
		"resource_group": c.opts.resourceGroup,
		"location":       loc,
		"subscription":   sub.ID,
		"sp_app_id":      sp.appID,
		"sp_tenant":      sp.tenant,
		"sp_secret_file": c.secretPath,
	}
	for _, k := range azManagedKeys {
		conf.set(k, values[k])
	}
	if sp.owned {
		conf.set(azKeySPCreated, "true")
	} else {
		conf.remove(azKeySPCreated)
	}
	if pending {
		conf.set(azKeySecretPending, "true")
	} else {
		conf.remove(azKeySecretPending)
	}
	// vm_size is the user's unless they asked for one (--vm-size) or the
	// default was not offered and connect picked another.
	if c.vmSizeSet || !conf.has("vm_size") {
		conf.set("vm_size", firstNonEmpty(c.vmSize, c.opts.vmSize))
	}
	if !conf.exists {
		conf.lines = append(conf.lines, "", azTextConfFileMarker)
	}
	after := string(conf.bytes())
	if conf.exists && after == before {
		if st, err := os.Stat(c.confPath); err == nil && st.Mode().Perm() != 0o600 {
			return true, os.Chmod(c.confPath, 0o600)
		}
		return false, nil
	}
	if err := azWriteFileAtomic(c.confPath, []byte(after), 0o600); err != nil {
		return false, err
	}
	conf.exists = true
	return true, nil
}

// writeBundle writes the embedded provider to bundleDir, rewriting only a
// file whose bytes or mode differ; reports whether anything changed.
func (c *azConnect) writeBundle() (bool, error) {
	changed := false
	if err := os.MkdirAll(c.bundleDir, 0o700); err != nil {
		return false, fmt.Errorf("creating %s: %w", c.bundleDir, err)
	}
	err := fs.WalkDir(azurevm.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(c.bundleDir, filepath.FromSlash(p))
		if d.IsDir() {
			if p == "." {
				return nil
			}
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return fmt.Errorf("creating %s: %w", dst, err)
			}
			return nil
		}
		data, err := azurevm.Files.ReadFile(p)
		if err != nil {
			return fmt.Errorf("reading the bundled %s: %w", p, err)
		}
		mode := os.FileMode(0o600)
		// The provider runs; demo-project scripts keep their exec bit so the
		// sandbox copy is executable (the provider copies X_OK as 0755).
		if p == "provider" || (path.Dir(p) == "demo-project" && strings.HasSuffix(p, ".sh")) {
			mode = 0o700
		}
		if st, err := os.Lstat(dst); err == nil && st.Mode().IsRegular() {
			old, rerr := os.ReadFile(dst) // #nosec G304 -- agent-deck's own bundled provider copy
			if rerr == nil && bytes.Equal(old, data) {
				if st.Mode().Perm() != mode {
					changed = true
					return os.Chmod(dst, mode)
				}
				return nil
			}
		}
		changed = true
		return azWriteFileAtomic(dst, data, mode)
	})
	if err != nil {
		return changed, fmt.Errorf("installing the bundled provider in %s: %w", c.bundleDir, err)
	}
	return changed, nil
}

// writeProviderConfig sets [providers.<name>] in config.toml and returns
// the entry the check uses.
func (c *azConnect) writeProviderConfig() (session.ProviderConfig, bool, error) {
	cfg, err := session.LoadUserConfig()
	if err != nil {
		return session.ProviderConfig{}, false, fmt.Errorf("loading config: %w", err)
	}
	command := c.bundledCommand()
	if pc := strings.TrimSpace(c.opts.providerCommand); pc != "" {
		command = pc
		if strings.ContainsRune(pc, filepath.Separator) {
			abs, err := filepath.Abs(pc)
			if err != nil {
				return session.ProviderConfig{}, false, fmt.Errorf("resolving --provider-command %q: %w", pc, err)
			}
			command = abs
		}
	}
	existing, ok := cfg.Providers[c.opts.name]
	if ok && existing.Command != "" && existing.Command != command && c.opts.providerCommand == "" {
		c.step("store", "ok", fmt.Sprintf(azTextKeepingCommandFmt, c.opts.name, existing.Command))
		return existing, false, nil
	}
	want := existing
	want.Command = command
	want.CreateTimeoutSeconds = 900
	want.TimeoutSeconds = 600
	if ok && want == existing {
		return existing, false, nil
	}
	// LoadUserConfig hands out the cached config: change a copy.
	next := *cfg
	next.Providers = maps.Clone(cfg.Providers)
	if next.Providers == nil {
		next.Providers = map[string]session.ProviderConfig{}
	}
	next.Providers[c.opts.name] = want
	if err := session.SaveUserConfig(&next); err != nil {
		return session.ProviderConfig{}, false, fmt.Errorf("saving config: %w", err)
	}
	return want, true, nil
}

// store writes the secret, azure-vm.conf, the bundled provider and
// config.toml. A new secret is bracketed by sp_secret_pending in the conf:
// conf (marker set), secret, conf (marker cleared). If the run stops in
// between, the next run sees the marker and does not keep the pair.
func (c *azConnect) store(conf *azConfFile, sub azSubscription, loc string, sp azSP) (session.ProviderConfig, error) {
	c.step("store", "start", "")
	stored := false
	confFailed := func(err error) error {
		return azFail(azKindStoreFailed, fmt.Sprintf("Error: saving %s: %v", c.confPath, err))
	}
	if sp.secret != "" {
		if _, err := c.writeConf(conf, sub, loc, sp, true); err != nil {
			return session.ProviderConfig{}, confFailed(err)
		}
		stored = true
		// The secret goes only into this 0600 file; az login reads it on
		// stdin ("@-"), so no trailing newline.
		if err := azWriteFileAtomic(c.secretPath, []byte(sp.secret), 0o600); err != nil {
			return session.ProviderConfig{}, azFail(azKindStoreFailed, fmt.Sprintf("Error: saving the secret: %v", err))
		}
	} else if st, err := os.Stat(c.secretPath); err == nil && st.Mode().Perm() != 0o600 {
		if err := os.Chmod(c.secretPath, 0o600); err != nil {
			return session.ProviderConfig{}, azFail(azKindStoreFailed, fmt.Sprintf("Error: %v", err))
		}
		stored = true
	}
	changed, err := c.writeConf(conf, sub, loc, sp, false)
	if err != nil {
		return session.ProviderConfig{}, confFailed(err)
	}
	stored = stored || changed
	if c.opts.providerCommand == "" {
		changed, err := c.writeBundle()
		if err != nil {
			return session.ProviderConfig{}, azFail(azKindStoreFailed, fmt.Sprintf("Error: %v", err))
		}
		stored = stored || changed
	}
	pc, changed, err := c.writeProviderConfig()
	if err != nil {
		return session.ProviderConfig{}, azFail(azKindStoreFailed, fmt.Sprintf("Error: %v", err))
	}
	stored = stored || changed
	c.changed = c.changed || stored
	msg := ""
	if stored {
		msg = fmt.Sprintf(azTextStoredFmt, c.confPath)
	}
	c.step("store", "ok", msg)
	return pc, nil
}

// check lists the group's sandboxes through the provider, as the SP. A
// just-created SP gets azConnectCheckWindow to become visible.
func (c *azConnect) check(pc session.ProviderConfig, sp azSP) (int, error) {
	c.step("check", "start", "")
	client := &provider.Client{Name: c.opts.name, Config: pc}
	tries := 1
	if sp.created {
		tries = int(azConnectCheckWindow/azConnectRetryEvery) + 1
	}
	var lastErr error
	for i := 0; i < tries; i++ {
		if i > 0 {
			if i == 1 {
				c.step("check", "warn", azTextWaitingForSP)
			}
			azConnectSleep(azConnectRetryEvery)
			if c.ctx.Err() != nil {
				break
			}
		}
		list, err := client.List(c.ctx)
		if err == nil {
			// The human CONNECTED block carries this line itself.
			if c.opts.jsonOut {
				c.step("check", "ok", azTextCheckPassed(c.opts.resourceGroup, len(list)))
			}
			return len(list), nil
		}
		lastErr = err
	}
	msg := azTextCheckFailed(c.opts.resourceGroup, azSaid("", lastErr))
	if sp.kept {
		msg += "\n" + azTextCheckFailedResetHint
	}
	return 0, azFail(azKindCheckFailed, msg)
}

// validateConf checks the azure-vm.conf values connect and disconnect pass
// to az, before any of them reaches argv.
func (c *azConnect) validateConf(conf *azConfFile) error {
	for _, k := range []struct {
		key string
		re  *regexp.Regexp
	}{
		{"sp_app_id", azGUIDRe},
		{"subscription", azGUIDRe},
		{"sp_tenant", azTenantRe},
		{"resource_group", azGroupRe},
		{"location", azLocationRe},
		{"vm_size", azSizeRe},
	} {
		if v := conf.get(k.key); v != "" && (!k.re.MatchString(v) || strings.HasPrefix(v, "-")) {
			return azFail(azKindBadConfig, azTextBadConfig(c.confPath, k.key, v))
		}
	}
	return nil
}

func (c *azConnect) connect() error {
	if err := c.findAz(); err != nil {
		return err
	}
	conf, err := azReadConf(c.confPath)
	if err != nil {
		return azFail(azKindStoreFailed, fmt.Sprintf("Error: %v", err))
	}
	if err := c.validateConf(conf); err != nil {
		return err
	}
	login, err := c.ensureLogin()
	if err != nil {
		return err
	}
	sub, err := c.pickSubscription(login.user)
	if err != nil {
		return err
	}
	// az uses its current login's directory; a subscription elsewhere needs
	// a login there (--tenant only picks among subscriptions az knows).
	if login.tenant != "" && !strings.EqualFold(login.tenant, sub.TenantID) {
		return azFail(azKindWrongTenant, azTextWrongTenantAccount(sub.Name, sub.ID, sub.TenantID, login.tenant))
	}
	if err := c.registerProviders(sub); err != nil {
		return err
	}
	groupLoc, groupExists, err := c.showGroup(sub)
	if err != nil {
		return err
	}
	loc, locPinned := c.regionCandidate(conf, sub, groupLoc, groupExists)
	size, sizePinned := c.opts.vmSize, c.opts.vmSizeSet
	if v := conf.get("vm_size"); v != "" && !c.opts.vmSizeSet {
		size, sizePinned = v, true
	}
	c.vmSize, c.vmSizeSet = size, c.opts.vmSizeSet
	if c.keepStoredSetup(conf, sub, groupExists, loc) {
		// A working stored setup: no probe resources on every run.
		c.step("region", "ok", fmt.Sprintf(azTextRegionKeptFmt, loc, size))
		if err := c.checkQuota(sub, loc, size); err != nil {
			return err
		}
	} else if loc, _, groupLoc, err = c.chooseRegion(sub, loc, locPinned, size, sizePinned, groupExists, groupLoc); err != nil {
		return err
	}
	sp, err := c.ensureSP(sub, conf)
	if err != nil {
		return err
	}
	c.checkScope(sub, sp.appID)
	pc, err := c.store(conf, sub, loc, sp)
	if err != nil {
		return err
	}
	n, err := c.check(pc, sp)
	if err != nil {
		return err
	}
	if c.opts.jsonOut {
		c.emit(map[string]any{"event": "done", "action": "connect", "resource_group": c.opts.resourceGroup,
			"subscription": sub.ID, "sp_app_id": sp.appID, "config": c.confPath, "provider": c.opts.name, "changed": c.changed})
	} else {
		fmt.Fprintln(c.out, azTextConnected(!c.changed, sub.Name, sub.ID, c.opts.resourceGroup, groupLoc, sp.appID, c.secretPath, c.confPath, n))
	}
	return nil
}

// ---- disconnect ----

func (c *azConnect) disconnect() error {
	conf, err := azReadConf(c.confPath)
	if err != nil {
		return azFail(azKindStoreFailed, fmt.Sprintf("Error: %v", err))
	}
	appID := conf.get("sp_app_id")
	if appID == "" && !azRegularFile(c.secretPath) {
		if c.opts.jsonOut {
			c.emit(map[string]any{"event": "step", "step": "sp", "status": "skip", "message": azTextNotConnected})
			c.emit(map[string]any{"event": "done", "action": "disconnect", "resource_group": conf.get("resource_group"),
				"subscription": conf.get("subscription"), "sp_app_id": "", "config": c.confPath, "provider": c.opts.name, "changed": false})
		} else {
			fmt.Fprintln(c.out, azTextNotConnected)
		}
		return nil
	}
	if err := c.validateConf(conf); err != nil {
		return err
	}
	rg := firstNonEmpty(conf.get("resource_group"), c.opts.resourceGroup)
	subID := conf.get("subscription")
	// Only an app connect created is deleted; one from --sp-app-id (or a
	// taken-over name) belongs to someone else.
	owned := appID != "" && conf.get(azKeySPCreated) == "true"

	// Ask before anything runs: no az call, no provider list.
	if !c.opts.yes {
		if !providerDeps.isTTY() {
			return azFailCode(azKindUsage, azTextDisconnectNeedsYes, 2)
		}
		if owned {
			fmt.Fprintf(c.errOut, azTextDisconnectConfirm, appID)
		} else {
			fmt.Fprintf(c.errOut, azTextDisconnectConfirmLocal, firstNonEmpty(appID, "(none)"))
		}
		answer, _ := c.in.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return azFail(azKindAborted, azTextDisconnectAborted)
		}
	}

	cfg, err := session.LoadUserConfig()
	if err != nil {
		return azFail(azKindStoreFailed, fmt.Sprintf("Error: loading config: %v", err))
	}
	if pc, ok := cfg.Providers[c.opts.name]; ok {
		client := &provider.Client{Name: c.opts.name, Config: pc}
		list, err := client.List(c.ctx)
		switch {
		case err != nil && !c.opts.force:
			return azFail(azKindListFailed, fmt.Sprintf(azTextListFailedFmt, c.opts.name, azSaid("", err)))
		case err != nil:
			c.step("check", "warn", fmt.Sprintf(azTextListFailedForcedFmt, c.opts.name, azSaid("", err)))
		case len(list) > 0 && !c.opts.force:
			ids := make([]string, 0, len(list))
			for _, sb := range list {
				ids = append(ids, sb.ID)
			}
			return azFail(azKindSandboxesExist, azTextSandboxesExist(len(list), rg, strings.Join(ids, ", ")))
		}
	}

	switch {
	case owned:
		if err := c.findAz(); err != nil {
			return err
		}
		login, err := c.ensureLogin()
		if err != nil {
			return err
		}
		if err := c.checkDisconnectTarget(login, appID, conf.get("sp_tenant"), subID); err != nil {
			return err
		}
		c.step("sp", "start", "")
		if subID != "" {
			scope := "/subscriptions/" + subID + "/resourceGroups/" + rg
			_, stderr, err := c.az("none", "role", "assignment", "delete", "--assignee", appID, "--scope", scope, "--subscription", subID)
			if err != nil && !azIsNotFound(stderr) {
				if azIsPermissionError(stderr) {
					return azFail(azKindNoPermissionSP, azTextNoPermissionSPDelete(appID, azSaid(stderr, err), subID, rg))
				}
				return azCallFailed("role assignment delete", stderr, err)
			}
		}
		_, stderr, err := c.az("none", "ad", "app", "delete", "--id", appID)
		if err != nil && !azIsNotFound(stderr) {
			if azIsPermissionError(stderr) {
				return azFail(azKindNoPermissionSP, azTextNoPermissionSPDelete(appID, azSaid(stderr, err), subID, rg))
			}
			return azCallFailed("ad app delete", stderr, err)
		}
		c.step("sp", "ok", fmt.Sprintf(azTextSPRemovedFmt, appID))
	case appID != "":
		c.step("sp", "skip", fmt.Sprintf(azTextSPNotOursFmt, appID))
	}

	c.step("store", "start", "")
	if err := c.removeLocal(conf, cfg); err != nil {
		return azFail(azKindStoreFailed, fmt.Sprintf("Error: %v", err))
	}
	c.step("store", "ok", azTextLocalRemoved)
	if subID != "" {
		c.step("group", "ok", fmt.Sprintf(azTextGroupKeptFmt, rg, rg, subID))
	}
	if c.opts.jsonOut {
		c.emit(map[string]any{"event": "done", "action": "disconnect", "resource_group": rg,
			"subscription": subID, "sp_app_id": appID, "config": c.confPath, "provider": c.opts.name, "changed": true})
	} else {
		fmt.Fprintln(c.out, azTextDisconnected)
	}
	return nil
}

// checkDisconnectTarget makes sure the az login is in the service
// principal's directory and reaches its subscription before anything is
// deleted. From another directory az answers "does not exist" for the app
// and its role assignments, which would look like a finished delete and
// leave the app in Azure while the local secret goes.
func (c *azConnect) checkDisconnectTarget(login azLogin, appID, spTenant, subID string) error {
	want := ""
	if azGUIDRe.MatchString(spTenant) {
		want = spTenant
	}
	if want != "" && !strings.EqualFold(login.tenant, want) {
		return azFail(azKindWrongTenant, azTextDisconnectWrongTenant(appID, want, login.tenant))
	}
	if subID != "" {
		out, stderr, err := c.az("json", "account", "show", "--subscription", subID)
		var sub struct {
			TenantID string `json:"tenantId"`
		}
		if err == nil {
			err = json.Unmarshal(out, &sub)
		}
		dir := firstNonEmpty(want, login.tenant)
		if err != nil {
			return azFail(azKindWrongTenant, azTextDisconnectWrongSubscription(subID, dir, appID, azSaid(stderr, err)))
		}
		if !azGUIDRe.MatchString(sub.TenantID) {
			return azFail(azKindAzFailed, fmt.Sprintf(azTextAzUnexpectedFmt, "account show", "tenant id", sub.TenantID))
		}
		if want == "" {
			// sp_tenant is a domain name: the subscription's directory decides.
			want = sub.TenantID
			if !strings.EqualFold(login.tenant, want) {
				return azFail(azKindWrongTenant, azTextDisconnectWrongTenant(appID, want, login.tenant))
			}
		} else if !strings.EqualFold(sub.TenantID, want) {
			return azFail(azKindWrongTenant, azTextDisconnectWrongSubscription(subID, want, appID,
				fmt.Sprintf(azTextSubInOtherTenantFmt, sub.TenantID)))
		}
	}
	if want == "" {
		return azFail(azKindBadConfig, azTextDisconnectTenantUnknown(c.confPath, appID))
	}
	return nil
}

// connectSecretElsewhere reports whether p, the sp_secret_file of
// azure-vm.conf, is another secret file connect wrote (say from an older
// config dir): an absolute path named like connect's, a regular file (not a
// symlink) with mode 0600. Anything else may be the user's and stays.
func connectSecretElsewhere(p, ours string) bool {
	if p == "" || !filepath.IsAbs(p) || filepath.Clean(p) != p || p == ours || filepath.Base(p) != azSecretFileName {
		return false
	}
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular() && st.Mode().Perm() == 0o600
}

func (c *azConnect) removeLocal(conf *azConfFile, cfg *session.UserConfig) error {
	if err := os.Remove(c.secretPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", c.secretPath, err)
	}
	if other := conf.get("sp_secret_file"); other != "" && other != c.secretPath {
		if connectSecretElsewhere(other, c.secretPath) {
			if err := os.Remove(other); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("removing %s: %w", other, err)
			}
		} else if _, err := os.Lstat(other); err == nil {
			c.step("store", "warn", fmt.Sprintf(azTextSecretElsewhereKeptFmt, other))
		}
	}
	if conf.exists {
		conf.remove(append(append([]string{}, azManagedKeys...), "vm_size", azKeySPCreated, azKeySecretPending)...)
		if conf.onlyOurs() {
			if err := os.Remove(c.confPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("removing %s: %w", c.confPath, err)
			}
		} else if err := azWriteFileAtomic(c.confPath, conf.bytes(), 0o600); err != nil {
			return err
		}
	}
	bundled := c.bundledCommand()
	// LoadUserConfig hands out the cached config: change a copy.
	providers := maps.Clone(cfg.Providers)
	if pc, ok := providers[c.opts.name]; ok && pc.Command == bundled {
		delete(providers, c.opts.name)
		next := *cfg
		next.Providers = providers
		if err := session.SaveUserConfig(&next); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
	}
	// The bundled copy stays while another entry still runs it.
	for _, pc := range providers {
		if pc.Command == bundled {
			return nil
		}
	}
	if st, err := os.Lstat(c.bundleDir); err == nil && st.IsDir() {
		if err := os.RemoveAll(c.bundleDir); err != nil {
			return fmt.Errorf("removing %s: %w", c.bundleDir, err)
		}
	}
	return nil
}
