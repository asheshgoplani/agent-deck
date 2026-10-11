# Black gaps between dialog options

## Finding

The New Session command selector switched from adjacent styled pills to pills
joined by a plain space in `8e535e229c79f136bbea2acd0c136ca63d4074b0`
(2026-09-18, `fix(ui): repair 9 rendering defects from the 2026-09-18 live UI audit`).
This first shipped in v1.16.11; v1.16.10 uses the old join. The change prevented
mid-word wrapping, but the separator follows a pill's SGR reset and therefore
uses the terminal's default background rather than the dialog's surface.
With a black terminal background, those one-cell gaps look like black squares.
They remain in v1.16.26 and v1.16.28's dark-theme output.

The committed ANSI golden for v1.16.26 independently contains six unpainted
separator cells on its first command row and five on the continuation row.
The light-theme-only reset fill added for #2449 does not cover dark theme.

## Source bisect

A no-checkout bisect avoids disturbing the working tree. This predicate locates
when the unstyled separators entered the command renderer; it is a source
predicate, not a live Kitty visual test:

```sh
git bisect start --no-checkout v1.16.11 v1.16.10
git bisect run bash -c '
  if git show BISECT_HEAD:internal/ui/newdialog.go |
    grep -Eq '\''(strings\.Join\(cmdButtons, " "\)|wrapIndented\(cmdButtons, " ")'\'';
  then exit 1; else exit 0; fi'
git bisect reset
```

The nine probes were: c988dc08 good, 386dc18 good, 9995c61 bad,
293ff430 good, 272c3a5 good, f58383a bad, c1c3695 bad, d0323a4 bad,
8e535e22 bad. Result: **8e535e22**.

A later change, 8d839ea2, moved the same plain separator into `wrapIndented`;
it did not introduce the original background defect.

## Fix and evidence

A single `renderPills` component in `internal/ui/pills.go` owns button styles,
surface-coloured separators, and optional whole-pill wrapping. New Session,
Edit Session, and account selectors all use it instead of independently
assembling styled fragments. Tool names and custom icons are resolved in one
place. This removes the three duplicated styling/spacing implementations while
retaining one-cell spacing and the existing word-wrap/indent behavior.

- `TestNewDialogCommandPillSeparatorsKeepSurfaceBackground` checks the actual
  dark-theme New Session view between the command pills.
- `TestSharedPillSeparatorsKeepSurfaceBackground` checks shared tool and label
  selectors.
- Both tests pass with the patch and fail with a Go overlay that restores the
  original unstyled separators. The failing view reports two single-space
  background holes; the shared rows each report one.
- Replaying the command renderer from `8e535e22^` in a Go overlay passes the
  view regression test; replaying the renderer from `8e535e22` fails with two
  exposed separator cells. Only the method signature is adapted to the
  current caller. This confirms the source bisect's renderer boundary.
- Existing dialog-focused regressions pass, including narrow wrapping and
  light-theme surface fill. A full UI-package run exceeded its 10-minute
  timeout while `TestIssue1397_HeadlessConcurrentMutationsNoRace` was running;
  the full package is not certified green on this host.
- The regenerated dark ANSI golden changes only the separator backgrounds
  on rows 26 and 27. Stripping SGR yields byte-identical visible content.
  Trailing spaces are intentional full-screen fixture padding.

This is ANSI-cell evidence, not a screenshot from a live Kitty instance.
Older background holes in input placeholders and trailing row padding are
outside this narrow inter-option fix.

## Local verification

Linking on this host's Btrfs-backed temporary directory repeatedly stalled.
RAM-backed scratch allowed the tests to finish:

```sh
scratch=$(mktemp -d /dev/shm/agent-deck-verify.XXXXXX)
GOTMPDIR="$scratch" GOMAXPROCS=4 go test ./internal/ui \
  -run 'Test.*(NewDialog|EditSessionDialog|WrapIndented|CommandRow|Pill|Issue2449)' \
  -count=1 -timeout=5m
rm -rf "$scratch"
```

Use an executable RAM-backed directory on hosts where `/dev/shm` is noexec.
