package issuereport

import (
	"bufio"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// Placeholders Scrub writes in place of what it removes. They are plain
// ASCII so they survive URL encoding and Markdown rendering unchanged.
const (
	PlaceholderToken = "<token>"
	PlaceholderEmail = "<email>"
	PlaceholderHost  = "<host>"
	PlaceholderIP    = "<ip>"
	PlaceholderUser  = "<user>"
	PlaceholderKey   = "<private-key>"
	PlaceholderMAC   = "<mac>"
)

var (
	privateKeyPattern = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`)
	// Any other PEM block (certificates, public keys) with both ends, and
	// a stray BEGIN/END line whose partner fell out of the log tail.
	pemBlockPattern = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?-----END [A-Z0-9 ]+-----`)
	pemFencePattern = regexp.MustCompile(`-----(?:BEGIN|END) [A-Z0-9 ]+-----`)
	// A line that is nothing but base64 (PEM body lines, key material).
	base64LinePattern = regexp.MustCompile(`(?m)^[ \t]*[A-Za-z0-9+/]{16,}={0,2}[ \t]*$`)
	// Consecutive <private-key> lines collapse into one.
	keyRunPattern = regexp.MustCompile(`(?m)^<private-key>(?:\n[ \t]*<private-key>[ \t]*)+$`)
	// Long base64 runs that may contain '/' and '+' (AWS secret access
	// keys, PEM fragments). The run must start after a character that
	// cannot be part of a path or URL, so ~/src/a/b and https://x/y are
	// never candidates; base64Secret decides on the shape.
	base64BlobPattern = regexp.MustCompile(`(^|[^A-Za-z0-9/+=~._:@<>-])([A-Za-z0-9+][A-Za-z0-9/+]{30,}={0,2})`)
	// mysql -u root -pSECRET (the password glued to -p).
	mysqlPassPattern = regexp.MustCompile(`\b(mysql|mysqldump|mysqladmin|mariadb(?:-dump|-admin)?)\b([^\n|;&]*?\s)-p("[^"\n]*"|'[^'\n]*'|[^\s'"-][^\s'"]*)`)
	// curl -u user:pass / --user user:pass (also glued: -uuser:pass).
	curlUserPattern = regexp.MustCompile(`\b(curl\b[^\n|;&]*?\s)(-u\s*|--user(?:\s+|=))("[^"\n]*:[^"\n]*"|'[^'\n]*:[^'\n]*'|[^\s'"<]*:[^\s'"]*)`)
	// --password X, --http-password X, --token X (the space form; the
	// = form is secretAssignPattern's). The value must not be a flag or
	// a placeholder.
	flagSecretPattern = regexp.MustCompile(`(^|\s)(--?[A-Za-z0-9-]*(?:passw(?:or)?d|passwd|pwd|token|secret|api-?key))([ \t]+)("[^"\n]*"|'[^'\n]*'|[^\s<'"-][^\s]*)`)
	// Slack / Discord webhook URLs: the path is the secret. The host is
	// bounded on both sides: on the left by the start of the text or a
	// character that cannot be part of a host label (any subdomain in
	// front, such as canary.discord.com, is part of the match), on the
	// right by an optional :port and then the path, so the dots are
	// literal and a webhook path behind a port or a versioned Discord
	// API (/api/v10/webhooks/) is still scrubbed.
	webhookPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9_])((?:[a-z0-9-]+\.)*(?:hooks\.slack(?:-gov)?\.com(?::[0-9]{1,5})?/(?:services|workflows|triggers)/|discord(?:app)?\.com(?::[0-9]{1,5})?/api/(?:v[0-9]+/)?webhooks/))[A-Za-z0-9/_-]+`)
	// A plain lowercase hex commit sha (40) or sha256 digest (64): an
	// identifier, not a secret.
	hexDigestPattern = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)
	// Runs the triggers.Redact backstop must not eat: hex digests and long
	// letter-only identifiers (CamelCase test names) with no digit.
	backstopKeep = regexp.MustCompile(`\b(?:[0-9a-f]{64}|[0-9a-f]{40})\b|[A-Za-z_]{40,}`)
	// MAC addresses (aa:bb:cc:dd:ee:ff / aa-bb-...).
	macPattern = regexp.MustCompile(`\b[0-9A-Fa-f]{2}(?:[:-][0-9A-Fa-f]{2}){5}\b`)

	// Token families (same set the GitHub sanitize gate and
	// triggers.Redact refuse), each replaced by <token>.
	tokenPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,})\b`),
		regexp.MustCompile(`\b(?:sk|pk|rk)-(?:[A-Za-z0-9]+-)*[A-Za-z0-9_-]{16,}`),
		regexp.MustCompile(`\bxox[abposre]-[A-Za-z0-9-]{8,}`),
		regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`),
		regexp.MustCompile(`\bPMAK-[A-Za-z0-9-]{20,}`),
		regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_-]{30,}\b`),                              // bot tokens
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,}`), // JWT
		// Vendor tokens that need no keyword next to them. Keep these
		// in step with the token list of the github sanitizer script.
		regexp.MustCompile(`\bgl(?:pat|dt|rt|ptt)-[A-Za-z0-9_-]{20,}`),             // GitLab
		regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}`),                                // Hugging Face
		regexp.MustCompile(`\b(?:sk|pk|rk)_(?:(?:live|test)_)?[A-Za-z0-9_-]{16,}`), // Stripe and other sk_/pk_ keys
		regexp.MustCompile(`\bshp(?:at|ss|ca|pa)_[A-Fa-f0-9]{32}\b`),               // Shopify
		regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43}`),           // SendGrid
	}
	// Telegram chat ids: chat_id, chat-id or chatid, then a number of 6+ digits.
	chatIDPattern = regexp.MustCompile(`(?i)\b(chat[_-]?id"?[ \t]*[=:]?[ \t]*"?)-?\d{6,}\b`)
	// "Authorization: Bearer xyz", "bearer xyz", "Basic xyz".
	authSchemePattern = regexp.MustCompile(`(?i)\b(bearer|basic|token)([ \t]+)[A-Za-z0-9._~+/=-]{8,}`)
	// key=value / key: value where the key names a secret.
	secretAssignPattern = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:token|secret|passw(?:or)?d|passwd|pwd|api[_-]?key|apikey|access[_-]?key|private[_-]?key|credential|auth|cookie|session[_-]?key)[A-Za-z0-9_.-]*)("?\s*[=:]\s*)("[^"\n]*"|'[^'\n]*'|[^\s,;&"'}]+)`)
	// scheme://user:pass@host
	urlCredPattern = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`)
	// Long mixed letter+digit runs: opaque keys we do not know the shape of.
	opaquePattern = regexp.MustCompile(`[A-Za-z0-9_+=-]{40,}`)

	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}\b`)
	// user@host (ssh/scp targets, shell prompts). The host must start with
	// a letter; version pins such as pkg@v1.2.3 / pkg@latest are skipped
	// in the replace func.
	userAtHostPattern = regexp.MustCompile(`([A-Za-z0-9._-]+)@([A-Za-z][A-Za-z0-9-]*(?:\.[A-Za-z0-9-]+)*)`)
	versionishPattern = regexp.MustCompile(`^(?:v\d.*|latest|next|main|master|HEAD)$`)
	// ssh/scp/mosh/rsync target after its options.
	// Stays on one line: a host on the next line is not ssh's.
	sshTargetPattern = regexp.MustCompile(`\b(ssh|mosh|autossh)([ \t]+(?:-[46AaCfGgKkMNnqsTtVvXxYy]+[ \t]+|-[BbcDEeFIiJLlmOoPpQRSWw][ \t]*\S+[ \t]+)*)([A-Za-z0-9_][A-Za-z0-9_.-]*)`)
	// ssh -l login (the login slot of ssh/mosh/autossh): "-l", a space and
	// the login, after ssh's own options and at most the host, on the same
	// line. "ls -la", "bash -lc", "wc -l" and "send-keys -l" later in a
	// line or on another line are not ssh's -l.
	sshLoginPattern = regexp.MustCompile(`\b((?:ssh|mosh|autossh)[ \t]+(?:-[46AaCfGgKkMNnqsTtVvXxYy]+[ \t]+|-[BbcDEeFIiJLmOoPpQRSWw][ \t]*[^\s;|&-][^\s;|&]*[ \t]+)*(?:[A-Za-z0-9_<][A-Za-z0-9_.@<>-]*[ \t]+)?)(-l[ \t]+)([A-Za-z0-9._][A-Za-z0-9._-]*)`)
	// host=..., hostname: ..., server=..., remote=...
	hostAssignPattern = regexp.MustCompile(`(?i)\b((?:remote[_-]?)?host(?:name)?|server|remote|ssh[_-]?host)("?\s*[=:]\s*"?)([A-Za-z0-9_][A-Za-z0-9_.-]*)`)
	// FQDNs ending in a real-world TLD (file names such as main.go or
	// debug.log never match because those suffixes are not in the list;
	// .app is left out so macOS bundles such as iTerm.app survive).
	fqdnPattern = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:com|net|org|io|dev|ai|co|me|info|biz|cloud|tech|xyz|online|site|host|server|lan|local|localdomain|internal|intranet|corp|home|arpa|us|uk|de|fr|nl|eu|ch|at|se|no|fi|dk|es|it|pl|cz|ru|jp|cn|in|br|ca|au|nz|tv|ts\.net)\b`)

	// user@<ip> left after the IP pass: the login still names someone.
	userAtIPPattern = regexp.MustCompile(`[A-Za-z0-9._-]+@(\[?<ip>)`)
	// login@1.2.3.4 before the ssh-target pass, which would otherwise
	// read the login as the target.
	userAtIPv4Pattern = regexp.MustCompile(`[A-Za-z0-9._-]+@((?:\d{1,3}\.){3}\d{1,3})\b`)

	// Host names in places only a host goes, whatever the name is.
	hostPortPattern   = regexp.MustCompile(`(?i)\b(host[ \t]+)([A-Za-z0-9_][A-Za-z0-9_.-]*)([ \t]+port\b)`)
	lookupPattern     = regexp.MustCompile(`(?i)\b(lookup[ \t]+)([A-Za-z0-9_][A-Za-z0-9_.-]*[A-Za-z0-9_])`)
	hostPathPattern   = regexp.MustCompile(`(^|[\s"'(=,\[])([A-Za-z0-9_][A-Za-z0-9_.-]*[A-Za-z0-9_]):(~|/[^/])`)
	schemeHostPattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([A-Za-z0-9_][A-Za-z0-9_.-]*)`)
	remoteQuoted      = regexp.MustCompile(`(?i)\b(remote[ \t]+)"([^"\s]+)"`)
	remoteColon       = regexp.MustCompile(`(?i)\b(remote[ \t]+)([A-Za-z0-9_][A-Za-z0-9_.-]*)(:)`)
	remoteVerb        = regexp.MustCompile(`(?i)\b(remote[ \t]+(?:add|rm|remove|delete|set|edit|update|show|attach|use|ping)[ \t]+)([A-Za-z0-9_][A-Za-z0-9_.-]*)`)
	toHostPort        = regexp.MustCompile(`(?i)\b(to[ \t]+)([A-Za-z][A-Za-z0-9_.-]*)(:\d)`)

	ipv4Pattern      = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\b`)
	ipv6Candidate    = regexp.MustCompile(`(?i)[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}(?:%[A-Za-z0-9]+)?`)
	wordPattern      = regexp.MustCompile(`\S+`)
	otherHomePattern = regexp.MustCompile(`(/home/|/Users/)[^/\s"'<>:]+`)
)

// publicDomains are well-known public domains that carry no private
// information and keep a report readable (the repo, Go modules, the model
// vendors). A name equal to one of them or under it is kept as-is.
var publicDomains = []string{
	"github.com", "githubusercontent.com", "golang.org", "go.dev",
	"anthropic.com", "claude.ai", "claude.com", "openai.com",
	"npmjs.com", "npmjs.org", "pypi.org", "brew.sh",
}

func isPublicHost(h string) bool {
	h = strings.TrimSuffix(strings.ToLower(h), ".")
	for _, d := range publicDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// notHostWords are words that sit where the host patterns look for a
// host but are ordinary log vocabulary ("lookup failed", "file:/x").
var notHostWords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "for": true, "to": true, "in": true, "on": true,
	"at": true, "by": true, "is": true, "and": true, "or": true, "from": true, "with": true,
	"failed": true, "failure": true, "error": true, "err": true, "errors": true, "timeout": true,
	"timed": true, "warning": true, "warn": true, "info": true, "debug": true, "note": true,
	"see": true, "file": true, "path": true, "dir": true, "cwd": true, "http": true, "https": true,
	"ssh": true, "git": true, "unix": true, "tcp": true, "udp": true, "localhost": true,
	"local": true, "command": true, "session": true, "sessions": true, "host": true, "hosts": true,
	"list": true, "status": true, "config": true, "origin": true, "upstream": true,
	"true": true, "false": true, "null": true, "nil": true, "none": true,
}

// sshProseWords follow "ssh" in prose rather than as its target ("ssh
// into the box", "my ssh remote session froze"); a configured host of
// that name is still replaced.
var sshProseWords = map[string]bool{
	"into": true, "onto": true, "to": true, "the": true, "a": true, "an": true, "and": true, "or": true,
	"remote": true, "session": true, "sessions": true, "connection": true, "connections": true,
	"tunnel": true, "access": true, "agent": true, "key": true, "keys": true, "login": true,
	"again": true, "works": true, "fails": true, "failed": true, "is": true, "was": true, "config": true,
}

// hostLike reports whether w, found where a host name goes, should be
// replaced: not a placeholder, an ordinary word, or a public domain.
func hostLike(w string) bool {
	if w == "" || strings.ContainsAny(w, "<>") || isPublicHost(w) {
		return false
	}
	if isConfiguredHost(w) {
		return true
	}
	return !notHostWords[strings.ToLower(w)]
}

// hostish reports whether a value after "host:" / "server=" / "remote:"
// reads as a host name rather than a word ("server: started", "host:
// false"): it has a '.', '-' or digit, or it is a configured host name.
func hostish(w string) bool {
	if !hostLike(w) {
		return false
	}
	return strings.ContainsAny(w, ".-0123456789") || isConfiguredHost(w)
}

// commonIdentityWords are names that are too common to scrub as bare
// words even when an ssh config or the machine uses them: replacing "git"
// or "dev" everywhere makes a report unreadable and hides nothing. They
// are still scrubbed where only a host or login goes (ssh dev, git@dev).
var commonIdentityWords = map[string]bool{
	"git": true, "dev": true, "mac": true, "claude": true, "codex": true, "gemini": true,
	"main": true, "master": true, "local": true, "localhost": true, "server": true, "host": true,
	"home": true, "test": true, "prod": true, "build": true, "staging": true, "work": true,
	"box": true, "vm": true, "linux": true, "ubuntu": true, "debian": true, "root": true,
	"admin": true, "user": true, "users": true, "ec2-user": true, "pi": true, "github": true,
	"gitlab": true, "origin": true, "upstream": true, "default": true, "docker": true,
	"router": true, "laptop": true, "desktop": true, "mini": true, "macbook": true,
	"remote": true, "agent": true, "deck": true, "session": true, "config": true,
	"agent-deck": true, "agentdeck": true,
}

// distinctiveName reports whether a configured name is safe to replace as
// a bare word: it has a '-', '.' or digit, or is 6+ characters, and is
// not a common word.
func distinctiveName(w string) bool {
	if commonIdentityWords[strings.ToLower(w)] {
		return false
	}
	return strings.ContainsAny(w, "-.0123456789") || len(w) >= 6
}

// localIdentity is the machine's own host name and login, scrubbed even
// when they appear as bare words. Overridable for tests.
var localIdentity = func() (hosts []string, users []string) {
	if h, err := os.Hostname(); err == nil && h != "" {
		hosts = append(hosts, h)
		if short, _, ok := strings.Cut(h, "."); ok && short != "" {
			hosts = append(hosts, short)
		}
	}
	if u := os.Getenv("USER"); u != "" {
		users = append(users, u)
	}
	if cu, err := user.Current(); err == nil && cu.Username != "" {
		users = append(users, cu.Username)
	}
	return hosts, users
}

// configuredNames are the hosts and logins this user has told agent-deck
// or ssh about.
type configuredNames struct {
	// hosts are real host names: agent-deck [remotes] names and hosts,
	// ssh HostName values, and ssh Host aliases that are the host's own
	// name (no HostName, or HostName equal to the alias). They are
	// replaced as bare words when distinctive.
	hosts []string
	// aliases are ssh Host aliases that point at another HostName. One
	// with a digit, '-' or '.' is replaced like a host; a letters-only one
	// ("Host backup office") is often an ordinary word and is replaced
	// only where a host goes.
	aliases []string
	// users are agent-deck remote logins and ssh-config User values.
	users []string
}

// configuredIdentity reads the names and hosts of agent-deck's [remotes]
// (session.LoadUserConfig) and the Host / HostName / User lines of
// ~/.ssh/config (no wildcard patterns). They are scrubbed as bare words
// too, so "buildmac-direct" in a title or log line does not leak even on
// a machine that is not buildmac-direct. Overridable for tests.
var configuredIdentity = func() (c configuredNames) {
	if cfg, err := session.LoadUserConfig(); err == nil && cfg != nil {
		for name, rc := range cfg.Remotes {
			c.hosts = append(c.hosts, name)
			h := strings.TrimSpace(rc.Host)
			// "local" names no ssh host (and is an ordinary word): nothing
			// to scrub.
			if h == "" || h == "local" {
				continue
			}
			if u, rest, ok := strings.Cut(h, "@"); ok {
				c.users = append(c.users, u)
				h = rest
			}
			if hh, _, err := net.SplitHostPort(h); err == nil {
				h = hh
			} else if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[:i], ":") {
				h = h[:i]
			}
			c.hosts = append(c.hosts, h)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		sc := sshConfigNames(filepath.Join(home, ".ssh", "config"))
		c.hosts = append(c.hosts, sc.hosts...)
		c.aliases = append(c.aliases, sc.aliases...)
		c.users = append(c.users, sc.users...)
	}
	return c
}

// sshConfigNames reads an ssh config: Host aliases (without wildcard or
// negated patterns) split into real names and aliases of another
// HostName, HostName values, and User values (common shared logins such
// as git or root are dropped later by loadIdentityLocked).
func sshConfigNames(path string) (c configuredNames) {
	f, err := os.Open(path) // #nosec G304 -- the user's own ssh config, read only
	if err != nil {
		return c
	}
	defer f.Close()
	type block struct {
		aliases  []string
		hostName string
	}
	var blocks []*block
	var cur *block
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, " ")
		if k2, v2, ok2 := strings.Cut(line, "="); ok2 && (!ok || len(k2) < len(key)) {
			key, val, ok = k2, v2, true
		}
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(val), "="))
		switch key {
		case "host":
			cur = &block{}
			blocks = append(blocks, cur)
			for _, h := range strings.Fields(val) {
				if !strings.ContainsAny(h, "*?!%") {
					cur.aliases = append(cur.aliases, strings.Trim(h, `"`))
				}
			}
		case "match":
			cur = nil
		case "hostname":
			if !strings.ContainsAny(val, "*?%") {
				hn := strings.Trim(val, `"`)
				c.hosts = append(c.hosts, hn)
				if cur != nil && cur.hostName == "" {
					cur.hostName = hn
				}
			}
		case "user":
			if u := strings.Trim(val, `"`); u != "" && !strings.ContainsAny(u, "%") {
				c.users = append(c.users, u)
			}
		}
	}
	// An alias is a real name when some block names it without pointing
	// it at another HostName.
	real := map[string]bool{}
	for _, b := range blocks {
		for _, a := range b.aliases {
			if b.hostName == "" || strings.EqualFold(b.hostName, a) {
				real[strings.ToLower(a)] = true
			}
		}
	}
	for _, b := range blocks {
		for _, a := range b.aliases {
			if real[strings.ToLower(a)] {
				c.hosts = append(c.hosts, a)
			} else {
				c.aliases = append(c.aliases, a)
			}
		}
	}
	return c
}

// identityTTL bounds how long the identity words are cached: the TUI runs
// for days and the user may add a remote meanwhile.
const identityTTL = time.Minute

var (
	identityMu sync.Mutex
	identityAt time.Time
	// identityHosts are replaced as bare words: this machine's names and
	// the distinctive configured ones. identitySlotHosts are every known
	// host name, replaced only where a host goes. identityUsers are
	// this machine's login, the logins of agent-deck [remotes] and the
	// ssh-config User values, minus common shared logins.
	identityHosts     []string
	identitySlotHosts map[string]bool
	identitySlotList  []string
	identityUsers     []string
)

// resetIdentity drops the cached identity words (tests).
func resetIdentity() {
	identityMu.Lock()
	identityAt = time.Time{}
	identityMu.Unlock()
}

func identity() ([]string, []string) {
	identityMu.Lock()
	defer identityMu.Unlock()
	loadIdentityLocked()
	return identityHosts, identityUsers
}

func loadIdentityLocked() {
	if !identityAt.IsZero() && time.Since(identityAt) < identityTTL {
		return
	}
	lh, lu := localIdentity()
	c := configuredIdentity()
	local := cleanIdentity(lh, true)
	configured := cleanIdentity(c.hosts, true)
	aliases := cleanIdentity(c.aliases, true)
	identitySlotHosts = map[string]bool{}
	var bare []string
	for _, h := range local {
		identitySlotHosts[strings.ToLower(h)] = true
		if !commonIdentityWords[strings.ToLower(h)] {
			bare = append(bare, h)
		}
	}
	for _, h := range configured {
		identitySlotHosts[strings.ToLower(h)] = true
		if distinctiveName(h) {
			bare = append(bare, h)
		}
	}
	for _, h := range aliases {
		identitySlotHosts[strings.ToLower(h)] = true
		if distinctiveName(h) && strings.ContainsAny(h, "-.0123456789") {
			bare = append(bare, h)
		}
	}
	identityHosts = cleanIdentity(bare, true)
	identitySlotList = cleanIdentity(append(append(append([]string(nil), local...), configured...), aliases...), true)
	var users []string
	for _, u := range cleanIdentity(append(lu, c.users...), false) {
		if !commonIdentityWords[strings.ToLower(u)] {
			users = append(users, u)
		}
	}
	identityUsers = users
	identityAt = time.Now()
}

// isConfiguredHost reports whether w is this machine's name or a host
// name or alias from agent-deck's [remotes] or the ssh config.
func isConfiguredHost(w string) bool {
	identityMu.Lock()
	defer identityMu.Unlock()
	loadIdentityLocked()
	return identitySlotHosts[strings.ToLower(w)]
}

// cleanIdentity dedups names, drops ones that are too short to replace
// safely, IP literals (the IP pass handles them), localhost and public
// domains, and orders longer names first.
func cleanIdentity(in []string, hosts bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range in {
		w = strings.TrimSpace(w)
		if len(w) < 3 || seen[w] || strings.ContainsAny(w, "<> \t") {
			continue
		}
		if hosts && (net.ParseIP(w) != nil || strings.EqualFold(w, "localhost") || isPublicHost(w)) {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// Scrub returns s with private data replaced by placeholders:
//
//   - paths under home (and any other /home/<name> or /Users/<name>) -> ~
//   - private key blocks -> <private-key>
//   - tokens (gh*_, github_pat_, sk-, xox*, AKIA, AIza, bot tokens, JWTs,
//     bearer/basic credentials, key=value secrets, URL credentials, long
//     opaque keys) -> <token>
//   - emails -> <email>
//   - user@host and ssh targets, FQDNs, host=... values, this machine's
//     host name -> <host>; this machine's login name -> <user>
//   - IPv4 and IPv6 addresses -> <ip>
//
// It reuses triggers.Redact (the repo's token redactor) as a final pass.
// Scrub is idempotent: scrubbing its output changes nothing. One pass can
// expose a new match (a replaced token next to a host name), so Scrub
// repeats the pass until nothing changes.
func Scrub(s, home string) string {
	for i := 0; i < maxScrubPasses; i++ {
		next := scrubOnce(s, home)
		if next == s {
			break
		}
		s = next
	}
	return s
}

// maxScrubPasses bounds Scrub's repeat; real text settles in two passes.
const maxScrubPasses = 8

func scrubOnce(s, home string) string {
	if s == "" {
		return s
	}
	home = strings.TrimRight(home, "/")
	if home != "" && home != "/" {
		s = replaceHome(s, home)
	}
	s = otherHomePattern.ReplaceAllString(s, "~")

	s = privateKeyPattern.ReplaceAllString(s, PlaceholderKey)
	s = pemBlockPattern.ReplaceAllString(s, PlaceholderKey)
	s = pemFencePattern.ReplaceAllString(s, PlaceholderKey)
	s = base64LinePattern.ReplaceAllStringFunc(s, func(m string) string {
		if mixedCaseDigits(strings.TrimSpace(m), 1) {
			return PlaceholderKey
		}
		return m
	})
	s = absorbKeyTails(s)
	s = keyRunPattern.ReplaceAllString(s, PlaceholderKey)
	s = urlCredPattern.ReplaceAllString(s, "${1}"+PlaceholderToken+"@")
	s = webhookPattern.ReplaceAllString(s, "${1}${2}"+PlaceholderToken)
	s = curlUserPattern.ReplaceAllString(s, "${1}${2}"+PlaceholderToken)
	s = flagSecretPattern.ReplaceAllString(s, "${1}${2}${3}"+PlaceholderToken)
	for _, p := range tokenPatterns {
		s = p.ReplaceAllString(s, PlaceholderToken)
	}
	s = chatIDPattern.ReplaceAllString(s, "${1}"+PlaceholderToken)
	s = mysqlPassPattern.ReplaceAllString(s, "${1}${2}-p"+PlaceholderToken)
	s = authSchemePattern.ReplaceAllString(s, "${1}${2}"+PlaceholderToken)
	s = secretAssignPattern.ReplaceAllStringFunc(s, func(m string) string {
		p := secretAssignPattern.FindStringSubmatch(m)
		v := strings.Trim(p[3], `"'`)
		switch strings.ToLower(v) {
		case strings.ToLower(PlaceholderToken), "", "bearer", "basic", "token":
			// Already scrubbed, or an auth scheme whose credential
			// authSchemePattern replaced.
			return m
		}
		return p[1] + p[2] + PlaceholderToken
	})

	s = emailPattern.ReplaceAllString(s, PlaceholderEmail)
	s = userAtHostPattern.ReplaceAllStringFunc(s, func(m string) string {
		p := userAtHostPattern.FindStringSubmatch(m)
		if versionishPattern.MatchString(p[2]) {
			return m
		}
		return PlaceholderUser + "@" + PlaceholderHost
	})
	s = userAtIPv4Pattern.ReplaceAllString(s, PlaceholderUser+"@${1}")
	s = sshLoginPattern.ReplaceAllString(s, "${1}${2}"+PlaceholderUser)
	s = sshTargetPattern.ReplaceAllStringFunc(s, func(m string) string {
		p := sshTargetPattern.FindStringSubmatch(m)
		if sshProseWords[strings.ToLower(p[3])] && !isConfiguredHost(p[3]) {
			return m // "ssh into the box", "my ssh remote session"
		}
		return p[1] + p[2] + PlaceholderHost
	})
	s = hostAssignPattern.ReplaceAllStringFunc(s, func(m string) string {
		p := hostAssignPattern.FindStringSubmatch(m)
		if p[3] == "host" { // "<host>" already, matched without the brackets
			return m
		}
		// host=x and "host":"x" are host slots; a bare "server: x" is
		// often prose ("server: started"), so only a host-ish x goes.
		strong := strings.Contains(p[2], "=") || strings.HasSuffix(p[2], `"`)
		if (strong && !hostLike(p[3])) || (!strong && !hostish(p[3])) {
			return m
		}
		return p[1] + p[2] + PlaceholderHost
	})

	s = macPattern.ReplaceAllString(s, PlaceholderMAC)
	s = ipv6Candidate.ReplaceAllStringFunc(s, func(m string) string {
		addr, _, _ := strings.Cut(m, "%")
		if strings.Count(addr, ":") < 2 {
			return m
		}
		if ip := net.ParseIP(addr); ip != nil && ip.To4() == nil {
			return PlaceholderIP
		}
		return m
	})
	s = ipv4Pattern.ReplaceAllString(s, PlaceholderIP)
	s = userAtIPPattern.ReplaceAllString(s, PlaceholderUser+"@${1}")
	s = fqdnPattern.ReplaceAllStringFunc(s, func(m string) string {
		if isPublicHost(m) {
			return m
		}
		return PlaceholderHost
	})

	hosts, users := identity()
	for _, h := range slotHosts() {
		s = replaceTmuxName(s, h, PlaceholderHost)
	}
	for _, h := range hosts {
		s = replaceWord(s, h, PlaceholderHost, !distinctiveName(h))
	}
	for _, u := range users {
		s = replaceWord(s, u, PlaceholderUser, !distinctiveName(u))
	}
	s = scrubHostSlots(s)

	s = base64BlobPattern.ReplaceAllStringFunc(s, func(m string) string {
		p := base64BlobPattern.FindStringSubmatch(m)
		if base64Secret(p[2]) {
			return p[1] + PlaceholderToken
		}
		return m
	})
	s = opaquePattern.ReplaceAllStringFunc(s, func(m string) string {
		if hexDigestPattern.MatchString(m) {
			return m
		}
		if strings.ContainsAny(m, "0123456789") && strings.IndexFunc(m, isLetter) >= 0 {
			return PlaceholderToken
		}
		return m
	})

	// Backstop: the repo's own token redactor (triggers.Redact), run per
	// word. Words with a "/" are skipped: its 48-char opaque-run rule
	// would otherwise eat long file paths. Its marker is mapped to ours
	// so the output uses one vocabulary.
	s = wordPattern.ReplaceAllStringFunc(s, func(w string) string {
		if strings.Contains(w, "/") {
			return w
		}
		return redactKeeping(w)
	})
	return s
}

// scrubHostSlots replaces names in places only a host name goes: "host X
// port", "lookup X", "X:/path", "X:~", scheme://X, remote "X", "remote X:",
// "remote add X", "to X:22". Public domains and ordinary words are kept.
func scrubHostSlots(s string) string {
	slot := func(re *regexp.Regexp, s string, rebuild func(p []string) string) string {
		return re.ReplaceAllStringFunc(s, func(m string) string {
			p := re.FindStringSubmatch(m)
			if !hostLike(p[2]) {
				return m
			}
			return rebuild(p)
		})
	}
	s = slot(hostPortPattern, s, func(p []string) string { return p[1] + PlaceholderHost + p[3] })
	s = slot(lookupPattern, s, func(p []string) string { return p[1] + PlaceholderHost })
	s = slot(hostPathPattern, s, func(p []string) string { return p[1] + PlaceholderHost + ":" + p[3] })
	s = slot(schemeHostPattern, s, func(p []string) string { return p[1] + PlaceholderHost })
	s = slot(remoteQuoted, s, func(p []string) string { return p[1] + `"` + PlaceholderHost + `"` })
	s = slot(remoteColon, s, func(p []string) string { return p[1] + PlaceholderHost + p[3] })
	s = slot(remoteVerb, s, func(p []string) string { return p[1] + PlaceholderHost })
	s = slot(toHostPort, s, func(p []string) string { return p[1] + PlaceholderHost + p[3] })
	return s
}

// mixedCaseDigits reports whether s has upper- and lower-case letters and
// at least minDigits digits.
func mixedCaseDigits(s string, minDigits int) bool {
	var up, lo, dg int
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			up++
		case r >= 'a' && r <= 'z':
			lo++
		case r >= '0' && r <= '9':
			dg++
		}
	}
	return up > 0 && lo > 0 && dg >= minDigits
}

// plainWordSegment matches a path segment that reads as a word ("Sources",
// "internal"), which random key material almost never contains.
var plainWordSegment = regexp.MustCompile(`^[A-Z]?[a-z]{3,}$`)

// base64Secret decides whether a base64 run (32+ chars, maybe with '/' or
// '+') is key material: mixed case plus a digit, and, when it has '/',
// no segment that reads as a plain word (so relative paths survive).
func base64Secret(m string) bool {
	if !mixedCaseDigits(m, 1) {
		return false
	}
	if strings.Contains(m, "/") {
		for _, seg := range strings.Split(m, "/") {
			if plainWordSegment.MatchString(seg) {
				return false
			}
		}
	}
	return true
}

// shortBase64Line is a short base64-only line (the last line of a PEM body).
var shortBase64Line = regexp.MustCompile(`^[A-Za-z0-9+/]{4,}={0,2}$`)

// absorbKeyTails replaces short base64-only lines that sit next to a
// <private-key> line: the short last line of a PEM body is key material
// too, but too short to judge on its own.
func absorbKeyTails(s string) string {
	if !strings.Contains(s, PlaceholderKey) {
		return s
	}
	lines := strings.Split(s, "\n")
	isKey := func(i int) bool { return i >= 0 && i < len(lines) && strings.TrimSpace(lines[i]) == PlaceholderKey }
	for changed := true; changed; {
		changed = false
		for i, l := range lines {
			t := strings.TrimSpace(l)
			if isKey(i) || !shortBase64Line.MatchString(t) || !(strings.ContainsAny(t, "0123456789+/=") || mixedCaseDigits(t+"0", 1)) {
				continue
			}
			if isKey(i-1) || isKey(i+1) {
				lines[i] = PlaceholderKey
				changed = true
			}
		}
	}
	return strings.Join(lines, "\n")
}

// replaceTmuxName rewrites agent-deck tmux session names that carry an
// identity word (agentdeck_<name>_<id>).
func replaceTmuxName(s, w, repl string) string {
	if len(w) < 3 || strings.ContainsAny(w, "<>") || !strings.Contains(s, "agentdeck_") {
		return s
	}
	re := wordRegexp(`agentdeck_` + regexp.QuoteMeta(w) + `($|[^A-Za-z0-9.-])`)
	return re.ReplaceAllString(s, "agentdeck_"+repl+"${1}")
}

func isLetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// replaceHome rewrites home and home/... to ~ and ~/..., but not a longer
// sibling such as /home/alice2 for home /home/alice.
func replaceHome(s, home string) string {
	re := wordRegexp(regexp.QuoteMeta(home) + `(/|\b|$)`)
	return re.ReplaceAllString(s, "~${1}")
}

// replaceWord replaces whole-word occurrences of w (len >= 3, so a short
// login such as "go" does not mangle prose). With dotGuard (for short or
// common names) a match inside a larger dotted name is not a whole word:
// "dev" in pkg.go.dev or "git" in .git stays (no '.' before it, and no
// '.' + name character after it; a sentence-ending '.' is fine). Without
// it a distinctive name is replaced next to a dot too
// (buildmac-direct.sock, agentdeck.buildmac-direct).
func replaceWord(s, w, repl string, dotGuard bool) string {
	if len(w) < 3 || strings.ContainsAny(w, "<>") {
		return s
	}
	re := wordRegexp(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(w) + `($|[^A-Za-z0-9_-])`)
	if dotGuard {
		re = wordRegexp(`(^|[^A-Za-z0-9_.-])` + regexp.QuoteMeta(w) + `($|[^A-Za-z0-9_.-]|\.(?:$|[^A-Za-z0-9_-]))`)
	}
	// Run twice: adjacent matches share a boundary character.
	for i := 0; i < 2; i++ {
		s = re.ReplaceAllString(s, "${1}"+repl+"${2}")
	}
	return s
}

// wordRegexps caches the per-name expressions Scrub builds (the identity
// words change rarely; Scrub runs per field and repeats until stable).
var wordRegexps sync.Map

func wordRegexp(expr string) *regexp.Regexp {
	if re, ok := wordRegexps.Load(expr); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(expr)
	wordRegexps.Store(expr, re)
	return re
}

// slotHosts returns every known host name (local and configured).
func slotHosts() []string {
	identityMu.Lock()
	defer identityMu.Unlock()
	loadIdentityLocked()
	return identitySlotList
}

// redactKeeping runs triggers.Redact on one word but keeps hex digests
// (commit shas, sha256) and long letter-only identifiers (test names),
// which its 40-hex and 48-char rules would otherwise replace.
func redactKeeping(w string) string {
	var kept []string
	masked := backstopKeep.ReplaceAllStringFunc(w, func(m string) string {
		kept = append(kept, m)
		const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
		i := len(kept) - 1
		if i >= len(letters) {
			return m // more than 26 kept words: the whole mask is dropped below
		}
		return "\x00" + letters[i:i+1] + "\x00"
	})
	if len(kept) > 26 {
		kept, masked = nil, w
	}
	out := strings.ReplaceAll(triggers.Redact(masked), "[redacted]", PlaceholderToken)
	for i, k := range kept {
		out = strings.Replace(out, "\x00"+string(rune('A'+i))+"\x00", k, 1)
	}
	return out
}
