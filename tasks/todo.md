# fix/preview-scale-20260918

- [x] 1. Layout root cause + scalable accounts table (golden frames 1/2/7/12 slots x 60/100/200)
- [x] 2. Usage feed by construction: hooks install wraps statusLine per slot; hooks status reports feed; preview names the reason
- [x] 3. Opt-in "ssh" field (system stats ssh_sessions via `who`, remote wire, header + preview, unknown first class)
- [x] 4. EXPLORE other optional fields at width 60 / long values -> issue drafts
- [x] Verify: go build/vet, Docker tests, on-screen frames (rc.6 vs branch) at 200/100
- [x] code-simplifier, commits, RESULTS.md, PR-BODY.md
