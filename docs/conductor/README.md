# Architecture diagrams

Editable [D2](https://d2lang.com) sources for the user-facing architecture diagrams, with the rendered SVGs committed alongside them. These replaced the AI-generated PNGs that previously lived in `documentation/assets/` and `docs/images/` — the PNGs had no editable source, so every product change meant a full re-generation.

| Diagram | Embedded in |
|---------|-------------|
| [`fleet-topology`](fleet-topology.d2) | `README.md`, `docs/CONDUCTOR-SETUP.md` |
| [`conductor-overview`](conductor-overview.d2) | `documentation/CONDUCTOR.md` |
| [`channels-topology`](channels-topology.d2) | `documentation/CONDUCTOR.md` |
| [`session-lifecycle`](../diagrams/session-lifecycle.d2) | `README.md` (Archive Sessions) |

## Regenerating

Install the [d2 CLI](https://d2lang.com/tour/install) (`go install oss.terrastruct.com/d2@latest` works), then:

```bash
cd docs/conductor
d2 fleet-topology.d2 fleet-topology.svg
d2 conductor-overview.d2 conductor-overview.svg
d2 channels-topology.d2 channels-topology.svg
cd ../diagrams
d2 session-lifecycle.d2 session-lifecycle.svg
```

Keep the `.d2` source and the `.svg` output in the same commit — the SVG is what renders on GitHub; the source is what makes the next edit cheap.

## Conventions

- Colors follow the Tokyo Night accents used by the README badges: blue `#7aa2f7` for user-facing flows, green `#9ece6a` for workers/delegation, yellow `#e0af68` for state/files/watchers, purple `#bb9af7` for infra/daemons, red `#f7768e` for escalations and destructive paths.
- Dashed red edges mark "never do this" or destructive flows; dashed purple marks periodic/injected flows.
- Keep labels short — these are maps, not prose. Details belong in the embedding doc.
