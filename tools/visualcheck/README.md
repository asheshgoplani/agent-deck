# Visual check

This tool drives the real agent-deck binary through a seeded gallery in a private tmux server and sandbox HOME. It captures 15 named frames at each of 80x24, 120x40 and 200x50, compares them with committed goldens, and writes `contact-sheet.html` and `visualcheck-report.json` in the working directory.

Run the complete package on the isolated test box through `g14-test.sh <repo-dir> -count=1 -timeout 30m ./tools/visualcheck`. This runs the unit tests and the real-binary walkthrough. The integration test builds the binary when `VISUALCHECK_BINARY` is unset. The standalone command is `go run ./tools/visualcheck <agent-deck-binary>` on Linux. `make visual-check` wraps that command.

To regenerate goldens on the test box, run `g14-test.sh <repo-dir> -run TestVisualCheckRegenerateGoldens -timeout 30m ./tools/visualcheck -args -visualcheck.regenerate`, copy the resulting goldens from the isolated checkout, and inspect every frame. `make visual-check-golden` is the local equivalent. **A diff needs a reviewer's PASS** before updated goldens are accepted.

A required screen that cannot be reached, is not captured, or differs from its golden fails the check. `advisoryReasons` in `capture.go` is the explicit exclusion list and is currently empty. Retries only recover navigation; exhaustion is an error. The report always includes one row per expected screen and width.

The gallery contains alpha and beta groups, a backend subgroup, idle/waiting/running/stopped/error sessions across tools, a fake remote, and a Hindi/emoji Claude response. Each width gets a fresh store and private tmux server. The update step installs a higher-version binary inside that sandbox, waits for the real installed-update banner, captures it, presses Ctrl+T, and confirms the restart. No production code or external update service is involved.

The scrubber replaces UUIDs, short IDs, semantic versions, clock times, ISO dates, relative ages, `just now`, the private tmux socket, sandbox paths, the shell prompt and startup echo, and the host name. `3m ago` becomes `<age> ago`. The sandbox header omits live CPU and disk measurements. Polls use screen predicates; short sleeps remain in navigation and tmux frame-settling code. Captures require stable, screen-specific text, then golden equality.
