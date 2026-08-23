# Context inspector gate notes

This directory contains development notes and partial SIXGATE inputs from the context
inspector work. It is **not** a completed six-gate example and does not carry a verdict.
In particular, the machine-readable G1–G5 results and `VERDICT.json`/`VERDICT.md` were
not committed, so `sixgate verdict context-inspector --check` cannot establish that the
evidence still stands. Under SIXGATE's rule, no transcript means not done.

The artifacts that are actually present are:

- [`G0-script.yaml`](G0-script.yaml), the scripted journey.
- [`RUN-2026-07-29.md`](RUN-2026-07-29.md), historical run notes.
- [`G3-matrix/matrix.yaml`](G3-matrix/matrix.yaml), the matrix declaration (not a
  completed matrix result).
- [`G4-oracle/oracle.yaml`](G4-oracle/oracle.yaml), the oracle declaration (not a
  parity result).
- [`G5-coldeye/resolutions.yaml`](G5-coldeye/resolutions.yaml), resolutions recorded
  during development (not a cold-eye outcome).

These files may help reconstruct a future run, but they must not be presented as proof
that all six gates passed. A completed example requires the generated evidence tree and
both verdict files to be committed together.
