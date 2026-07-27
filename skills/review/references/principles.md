Shared vocabulary for the `design` architecture pass, the `tdd` refactor step, and the adversarial review layer: use these four lenses to name what's actually wrong in a diff, not to enforce style for its own sake.

## DRY

Means eliminating duplicated *knowledge*, not duplicated characters — the same rule or decision should live in one place.

- copy-pasted logic drifting apart: two near-identical blocks that started as one copy-paste and have since diverged, so a fix to one silently misses the other
- the same business rule (a threshold, a status mapping) hardcoded in multiple files instead of one source of truth
- a bug fixed in one copy of duplicated code but not its sibling

## KISS

Means choosing the most direct implementation that satisfies the requirement, not the cleverest one.

- a needless abstraction layer: an interface, factory, or wrapper with exactly one implementation and no second caller in sight
- indirection (config-driven dispatch, generic plugin hooks) standing in for what could be a direct call or a plain conditional
- boolean-flag parameters that silently switch a function between two unrelated behaviors instead of being two functions

## YAGNI

Means building for the requirement in front of you, not the one you're guessing at.

- speculative generality: extension points, config options, or parameters with no current caller
- an abstract base class or plugin system built for a second implementation that doesn't exist yet
- unused error-handling branches or feature flags for scenarios nothing in the codebase can trigger

## SOLID

Means each unit has one reason to change and depends on abstractions, not on other units' internals.

- god-object / SRP breaks: a class or module that mixes unrelated responsibilities (parsing, persistence, and presentation in one file) and grows on every unrelated feature
- a caller reaching into another module's internals or concrete type instead of depending on its public interface
- a new subtype forcing edits to a shared switch/if-chain instead of extending independently

A principle is a lens, not a rule to enforce against the user's stated requirements. An abstraction the spec explicitly asked for is not YAGNI, and inlining duplicated logic twice is not automatically a DRY violation.
