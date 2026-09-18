# fix/preview-scale-20260918

- [x] 1. Layout root cause + scalable accounts table (golden frames 1/2/7/12 slots x 60/100/200)
- [x] 2. Usage feed by construction: hooks install wraps statusLine per slot; hooks status reports feed; preview names the reason
- [x] 3. Opt-in "ssh" field (system stats ssh_sessions via `who`, remote wire, header + preview, unknown first class)
- [x] 4. EXPLORE other optional fields at width 60 / long values -> issue drafts
- [x] Verify: go build/vet, Docker tests, on-screen frames (rc.6 vs branch) at 200/100
- [x] code-simplifier, commits, RESULTS.md, PR-BODY.md

## Round 2 (review HOLD aa00ef70)

- [x] 1. `usage ingest` never fails closed: store open/parse/save errors are stderr warnings, wrapped command always runs (bytes + exit code forwarded); `hooks install` refuses a slot the quota cache cannot name, `hooks status` says `cannot wire (...)`
- [x] 2. `wrapPreviewLine`: a clause that fits neither the current line nor a continuation line behind its lead is word-wrapped; every line ≤ width, second pass is the identity (no blank line)
- [x] 3. Single-line fields share the row budget: wrapped lines capped with `…` so later fields and the hint stay on a short pane
- [x] 4. `who` under `LC_ALL=C`
- [x] 5. `hooks install` never creates a slot's config dir: `skipped: slot dir missing`
- [x] 6. Pinned absolute path: keep (a); shell fallback double-runs the status line on a forwarded non-zero exit (proven); documented
- [x] 7. `hooks uninstall` removes only the keys install wrote; the pre-install object comes back
- [x] 8. Wrapped command sees `AGENTDECK_PROFILE` as inherited, not the wrapper's `-p`
- [x] Verify: go build/vet, Docker tests, temp-HOME proofs, on-screen frames w45/h30 and w60/h20, code-simplifier
