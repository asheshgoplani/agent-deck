# fix/never-prefer-empty-xdg-store-20260920 (round 2, review HOLD on 4e84dd1d)

- [x] 1. Root rule is marker-based, not count-based: `profiles/.active-root` (written by `migrate-paths`) decides when both roots are populated; no marker -> legacy + WARN; the only automatic protection is empty-beside-populated = stray -> populated root; matrix documented; post-migration `remove` + profile create keeps XDG (test + probe)
- [x] 2. `store_selected` / `stray_xdg_store` actually emitted: structured line after `logging.Init` (TUI, notify daemon), one stderr WARN per CLI process (not hook-handler/completion/doctor/health); tests
- [x] 3. Unreadable store = unknown, never 0: never loses to an empty store; prefer the readable populated root + WARN; doctor prints `unreadable`; test
- [x] 4. doctor counts rows in the clean layout too; test
- [x] 5. Remedies that work: stray WARN says "move the stray profiles/ aside"; refusal error names the real fix; `migrate-paths` writes the marker (plain and --force); end-to-end sandbox test legacy -> XDG then the CLI uses XDG
- [x] 6. `--group`/`--select` store open before the guards: note (kept before the no-TTY gate on purpose, #2011; the create guard makes it harmless)
- [x] CHANGELOG + config-reference + PR body; go build/vet/gofmt; incident reproductions re-run; CI; RESULTS.md
