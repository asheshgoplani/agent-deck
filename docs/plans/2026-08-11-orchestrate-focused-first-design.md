# Orchestrate focused-first design

## Motivation

The orchestrate workflow can spend more effort specifying, reviewing, and
repairing an implementation plan than implementing the approved work. A
planner that embeds production code and duplicates the full design creates a
shadow implementation: it consumes large contexts, attracts code-review
findings before real code exists, and can trigger planner rotation and review
loops.

Orchestration should still plan when coordination or execution risk makes a
shared technical contract valuable. The goal is not to eliminate planning; it
is to make planning an evidence-based exception and keep its output concise.

## Decisions

### Focused-first routing

An approved design or specification defaults to one strong implementation
worker that owns the change end to end. Task size or the existence of a design
document alone does not require a planner.

The conductor may launch a planner when it records at least one concrete
planning trigger in the run manifest:

- two or more workers require disjoint ownership boundaries or shared
  interfaces;
- database schemas, migrations, APIs, events, or cross-service contracts must
  be settled before implementation diverges;
- the work is destructive, irreversible, security-sensitive, or unusually
  difficult to recover;
- several dependent changes have non-obvious ordering;
- one implementation session would realistically exceed its context budget;
- multiple technically meaningful approaches remain after product design.

The record must name the trigger and the decision the plan needs to settle.
Generic labels such as “large task” or “complex feature” are insufficient.

### Concise coordination plans

A plan describes coordination, not hypothetical implementation. It contains:

- task ownership and scope;
- relevant file or subsystem paths;
- dependencies and ordering;
- interfaces consumed and produced;
- acceptance criteria;
- verification commands and required evidence;
- safety and rollback steps when applicable.

Plans and task boundaries must not embed production implementation, duplicate
the approved design verbatim, or predict exact test output that has not been
observed. Short signatures, schemas, and pseudocode are allowed when they are
the interface being coordinated.

Each implementer and reviewer receives the approved design plus its concise
task boundary. The plan does not replace the approved design as the source of
truth.

### Bounded plan review

Plan review applies only when a plan coordinates multiple implementers or
defines a high-risk shared contract. It checks requirement coverage, task
overlap, ordering, interface consistency, safety gates, and verification
coverage. It does not review speculative code quality.

There is one review and at most one planner amendment. The amended plan is not
reviewed again. Design-level contradictions return to the user; local
implementation details are resolved by the implementer and reviewed against
real code.

If planning exhausts its context, produces an implementation-sized artifact,
or receives findings across most tasks, the conductor does not rotate through
more planners. It collapses to one strong implementation worker unless the
recorded coordination or safety trigger makes that unsafe. In that exceptional
case it stops and asks the user to resolve the blocking architectural decision.

### Safety checklists remain small

Destructive and production-facing changes retain explicit target guards,
snapshot or rollback evidence, dry-run behavior where available, apply steps,
and post-write verification. These form a concise execution checklist; they do
not justify embedding complete scripts or tests in a plan.

## Interfaces and documentation changes

- `skills/orchestrate/SKILL.md` will route design/spec inputs through the
  focused-first gate and define the concrete planning triggers.
- The planning and plan-review sections will enforce the bounded workflow and
  fallback behavior.
- `skills/orchestrate/references/prompts/plan.md` will request concise task
  boundaries instead of actual code and verbatim design extracts.
- Relevant skill tests will lock the focused-first routing, planning triggers,
  concise planner contract, and one-amendment limit.

## Out of scope

- Implementation code review, fix rounds, PR creation, CI supervision, and
  deployed-system verification are unchanged.
- This does not prohibit parallel execution when work is genuinely independent.
- This does not remove technical design. It prevents reopening approved product
  design and limits technical planning to decisions implementation depends on.

## Principles check

The design removes duplicated design text and speculative code from plans
(DRY), defaults to the most direct delivery path (KISS), retains planning only
for stated coordination or risk requirements (YAGNI), and keeps product design,
technical coordination, implementation, and review as distinct responsibilities
(SOLID).
