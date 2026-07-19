# security-self-scan.md — the maintainer's security sweeps, author-side

> Before a merge wave, the maintainer's pipeline runs a hidden-logic sweep and a
> set of red-flag checks over community diffs. This file is the same sweep
> rewritten as greps you run on your own branch before opening the PR, so
> nothing in your diff surprises the security lens. `scripts/self-check.sh`
> automates most of these; this is the full manual list with the reasoning.
>
> All commands assume: `MB=$(git merge-base HEAD origin/main)`.

Helper — added lines only:

```bash
added() { git diff "$MB" HEAD -- "$@" | grep '^+' | grep -v '^+++' | sed 's/^+//'; }
```

## 1. Invisible characters and homoglyphs (automatic reject)

Zero-width, RTL-override, and look-alike Unicode in code are backdoor tooling.
Legit em-dashes in comments and glyphs inside quoted UI strings are fine —
confirm each hit is one of those.

```bash
added | perl -ne 'print "$.: $_" if /[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}\x{FEFF}]/'   # must print nothing
added -- '*.go' | perl -ne 'print "$.: $_" if /[^\x00-\x7F]/'                                  # confirm each hit
```

## 2. Downloaded-and-executed code (automatic stop-for-human)

```bash
added | grep -nE 'curl[^|;&]*\|[[:space:]]*(ba|z)?sh|wget[^|;&]*\|[[:space:]]*sh'   # must print nothing
added | grep -nE 'base64 (-d|--decode)'                                             # confirm each hit
```

Documentation that quotes a hunted pattern (like this file) is the
confirmed-benign case; `self-check.sh` scopes its automated pattern scans to
non-markdown code lines for exactly that reason.

## 3. Network calls in production paths

No unexpected outbound network in non-test code. Test fixtures (`example.com`)
and Unix-socket tmux operations are fine; anything else needs a sentence of
justification in the PR body, and the destination must be user-configured, never
hardcoded.

```bash
added -- '*.go' ':!*_test.go' | grep -nE 'net\.Dial|http\.(Get|Post|NewRequest)|websocket|smtp\.'
```

## 4. Exec of constructed strings

`exec.Command` on a built string is the classic injection surface. Arguments
must be validated and passed via argv or STDIN — never a shell-interpolated
string. Paths get shell-quoted and traversal-guarded (`..` and separators
rejected).

```bash
added -- '*.go' | grep -nE 'exec\.Command(Context)?\([^)]*(\+|fmt\.Sprintf)'
added -- '*.go' | grep -nE '"(ba|z)?sh",[[:space:]]*"-c"'    # read each in full
```

## 5. Environment / secret exfiltration

`os.Getenv` for PATH, documented feature flags, and test shims are fine. Reads
of tokens/keys/credentials that then travel toward any network call are not.

```bash
added -- '*.go' | grep -nE 'os\.Getenv\([^)]*(TOKEN|KEY|SECRET|CRED|AUTH)'
```

## 6. File permissions

Secrets and control directories are `0600`/`0700`. World-writable modes and
permission-broadening `Chmod` calls get rejected.

```bash
added | grep -nE '0o?777|0o?666'          # must print nothing (or justify each)
added -- '*.go' | grep -nE 'Chmod'        # confirm each never broadens
```

## 7. Time-bombs and weak crypto

Hardcoded-date gates are only acceptable as fixed clocks inside `_test.go`.

```bash
added -- '*.go' ':!*_test.go' | grep -nE 'time\.Now\(\)\.Unix\(\)[[:space:]]*>[[:space:]]*[0-9]{6,}'
added -- '*.go' | grep -nE 'InsecureSkipVerify|crypto/(md5|rc4|des)'
```

## 8. Config injection

Code that writes user config (e.g. `config.toml`) must source entries only from
the user's own registry — never inject a hardcoded external server, URL, or
command.

```bash
added -- '*.go' | grep -nE '(config\.toml|WriteConfig|SaveUserConfig)' | grep -nE 'https?://'
```

## 9. Dependency hygiene (if go.mod/go.sum changed at all)

A community PR that adds or bumps a dependency is unusual and gets extra
scrutiny by policy. Pre-answer the questions: why does this need a new module?
is the path the real, canonical upstream (no typosquats, no look-alike orgs)?
is it pinned? is it actually used?

```bash
git diff "$MB" HEAD -- go.mod | grep -E '^\+\s*replace\s'      # must print nothing — automatic stop
added | grep -n 'GOPROXY'                                       # must print nothing — automatic stop
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go mod verify   # "all modules verified"
# optional but appreciated as PR evidence:
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= govulncheck ./...
```

## 10. Workflow safety (if you must touch `.github/` — separate PR, human-gated)

- Never introduce or widen `pull_request_target` / `workflow_run` /
  `issue_comment` triggers; never check out PR-head code under an elevated token.
- Never interpolate attacker-controlled fields (`${{ github.event.pull_request.title }}`
  etc.) inside a `run:` script — pass via `env:` and reference as quoted shell vars.
- Third-party actions (anything not `actions/*` or `github/*`) are pinned by full
  commit SHA, never a mutable tag; converting a SHA pin back to a tag is rejected.
- No `permissions:` block granting `write` to a job that runs untrusted code.
- No new outbound `curl`/`wget` to external hosts.

## Reading the results

Every hit is either **removed**, **confirmed-benign with one sentence in the PR
body saying why** ("the `http.Get` is the new SSE status endpoint, host comes
from the user's session config"), or it becomes a review flag against your PR.
The security lens reads each hit in full — pre-explaining costs one sentence;
being discovered costs a review round.
