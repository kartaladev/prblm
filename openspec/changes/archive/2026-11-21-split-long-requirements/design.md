# Design

## Context

See proposal.md, "Why". The five requirements are in `openspec/specs/` on `main` after the `bounded-error-walk` archive. `openspec validate --specs` passes; `--strict` fails on `error-mapping`, `fiber-adapter`, `problem-response` and `validation-error-mapping` with "Requirement text is very long (>500 characters)". Tests cite scenarios as `<capability> / Scenario: <name>`. One test comment also names a requirement: `adapters/fiberprblm/handler_test.go` (around line 951) names "Fiber errors are status hints without their message".

## Goals / Non-Goals

**Goals:**
- Every one of the five becomes requirements of one behavior each, at most 500 characters.
- Every scenario keeps its name and content. Each one sits under the requirement whose behavior it tests.
- `openspec validate --specs --strict` passes after the archive.

**Non-Goals:**
- Changing any rule's meaning, Examples row or scenario step.
- Fixing gaps found in a moved scenario. For example, "Route-not-found check cannot finish" has no Examples table; it moves as it is.
- Reworking long requirements that the validator does not flag.

## Slices

1. **Deltas validate, specs pass strict**:
   - At the end of this slice, `openspec validate split-long-requirements --strict` passes.
   - After the archive's sync, `openspec validate --specs --strict` passes, and every moved scenario still appears once, verbatim.
   - No code path changes, so the slice introduces no hot path. The tests that cite the moved scenarios run unchanged.

## Decisions

### D1. Tighten when the split would move Examples rows; otherwise split

- "Error values with panicking methods never break resolution" (502 characters) and "Translator panics are contained once" (652) are MODIFIED with tighter text. The words dropped restate what the Examples rows already show: "so the handler's `OnError` hook can find it", "not by the value `recover` returns", "as a `*prblm.PanicError` with a nil value", "as the core does".
- The other three state several behaviors, each with its own scenarios, so they are split.
- **Alternatives:**
  - Splitting all five. For "Translator panics are contained once", the `http.ErrAbortHandler` rule shares its only scenario with the containment rules. A split would move a row into a new scenario and change a test's mapping.
  - Tightening all five. "Response headers" (1,006 characters) cannot reach 500 without dropping rules.
- **Failure modes:**
  - Tightened text drops a rule. Each dropped phrase is checked against an Examples row that still states it; tasks.md lists the check.

### D2. A split is REMOVED plus ADDED, under new names

- **What OpenSpec 1.17.0 rejects:**
  - A MODIFIED block that drops scenarios. It reports "omits scenario(s) the current spec still has" even when the scenarios are in an ADDED requirement of the same delta.
  - A name in both REMOVED and ADDED: "Requirement present in both ADDED and REMOVED".
- So each split requirement is REMOVED, with a Reason and a Migration that name its parts, and its parts are ADDED under new names. The part that keeps the original rule gets a name close to the old one: "Fiber error codes are status hints", "Opt-in recovery turns panics into errors", "Default response headers".
- **Alternatives:**
  - RENAMED plus MODIFIED. It hits the same drop check.
  - Keeping all scenarios under the shortened MODIFIED requirement and adding scenario-less requirements. That is invalid: every requirement needs a scenario.
- **Failure modes:**
  - A reference to an old name is left dangling. Searching outside the archived changes found one, the fiberprblm test comment, and the change updates it.
  - The archive's sync appends the ADDED requirements at the end of `## Requirements`. They sit apart from where the old one was, which only affects reading order.

### D3. A schema rule yields to the request

- The schema's spec instruction says that under MODIFIED, existing text is never trimmed "just to meet the length". This change exists to do exactly that, at the maintainer's request (the follow-up recorded in PR #23).
- D1 limits the trimming to restatements of Examples rows.

## Defaults

Unchanged; see proposal.md, "Defaults and Customization".

## Hot Paths

None changed. The scenarios that cover the existing hot paths keep their names, so their tests still map to them:
- resolution with panicking error methods
- fiber hints and recovery
- the header merge
- validatormap's containment

## Risks / Trade-offs

- [The renames break an outside link to a requirement name] → Requirement names are not part of the Go API. The repository's only reference outside archived changes is updated.
- [Reviewers miss a moved scenario] → A task checks that each of the 14 moved or kept scenarios appears exactly once in the synced main specs, byte for byte.
