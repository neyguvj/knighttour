# AGENTS.md

## Mandatory workflow (read first)

- Before code modification
  1. read specs for certain module `specs/` folder
  2. create it if it does not exist
  3. update specs before code modification
  4. Use updated specs to make changes and tests for new code
- Post-Change Verification: After every code modification, you MUST run `make check`
  (it runs: go fmt → go vet → go test -race → auto-fix modern idioms → golangci-lint).
  Do not report completion if it fails. Individual targets: `make fmt vet test lint fix bench`.

## Spec structure (one fact = one place)

- `specs/<pkg>.md` — **current reference implementation only**: responsibility, public API +
  contracts, invariants, edge cases, test requirements. NO history, NO benchmark tables, NO
  code/examples from other modules, NO design rationale.
- `specs/decisions/NNN-*.md` (ADRs) — why a decision was made; **measurements live here forever**
  (each change brings its own before/after numbers). Module specs link `(ADR-0NN)`.
- `specs/plans/NN-*.md` — not-yet-canonical optimization ideas (hypothesis → design → steps →
  success metrics+threshold → risks → specs touched). On acceptance → spec + ADR.
- `specs/benchmarks.md` — benchmark methodology. `docs/requirements.md` — task spec & reference numbers.
- Authoring guides are skills: `spec-writing`, `plan-writing` (loaded when editing `specs/`),
  `readme-writing` (loaded when editing `README.md`).

## README (showcase)

README = current state + final conclusions, a narrative article with tables from the single
benchmark. Update it in the same change **only** when a trigger fires: CLI flags/defaults changed,
pipeline structure changed, or a benchmarker verdict moves defaults/optimum/conclusions. Minor
optimizations and their before/after numbers live in ADRs — the showcase stays untouched.
Rules, table procedure (`make bench-table`) and freshness stamp: skill `readme-writing`.

## Automation pipeline (escalation ladder)

- `/quick` – tiny code-only fix, no spec change.
- `/task`  – lightweight spec-first in one context (specs updated before code).
- `/feature` – full pipeline behind a **thin orchestrator**: it never reads sources or specs and
  never reasons about the domain — it only dispatches subagents, relays their `QUESTION:` blocks to
  the user, tracks phases in `todowrite`, and commits. Phases: interview + spec/plan update by the
  `spec` subagent → **coder ⇄ reviewer loop** until `VERDICT: APPROVED` (max 5 iters, sessions
  resumed by task_id) → `benchmarker` writes ADR measurements for hot-path changes → commit on
  explicit user confirmation. No handoff files and no spawned sessions: the next task starts in the
  same session (one session = many tasks; heavy work lives in subagent contexts, so the orchestrator
  stays cheap). Subagents: `spec` (interview + specs/plans/ADR edits), `coder` (implements to green
  `make check`), `reviewer` (read-only, severity BLOCKER/MAJOR/MINOR + `SPEC_OK`), `benchmarker`
  (WIN/REGRESSION/NOISE, records numbers in ADR). Restart opencode after editing agent/skill/command files.

## Agent question protocol (relay)

Subagents have no direct line to the user: they talk through the orchestrator. When a subagent needs
a decision it cannot make safely, it ENDS its turn with one block per question:

```
QUESTION: <single-line question>
OPTIONS: <opt1> | <opt2>            # optional, when choices are known
CONTEXT: <1–3 lines needed to answer>   # optional
```

Rules: max 4 blocks per turn (batch what belongs together); everything not blocking is a decision
the subagent makes itself and records in `NOTES`. The orchestrator asks the user verbatim via the
`question` tool and resumes that subagent with answers only (`ANSWER: …`). A subagent that needs no
user input finishes with its report block (see its agent file). Never fake an answer.

## Quick Start

```bash
go run main.go        # Build & run
make check            # fmt + vet + test -race + auto-fixes + linter
make bench            # Benchmarks (counter/)
```

## Chat settings
- Answer in Russian language.
- Write code and comments in English

## Environment & Version Awareness
- Detect Version: Read go.mod before writing code. 
- Target Version: Assume Go 1.26+ unless stated otherwise in go.mod.

## Workflow
- Before code modification
  1. read specs for given `specs/` folder
  2. create it if it does not exist
  3. update specs before code modification
  4. Use updated specs to make changes and tests for new code
  5. optimization proposals live in `specs/plans/` — one numbered file per idea
     (hypothesis → design → steps → success metrics → risks → specs touched)
- Post-Change Verification: After every code modification, you MUST run `make check`.
  If any optimization or hot-path change is made, ALSO run `make bench` and attach
  before/after numbers — never claim a performance win without measurements.

## Modern Go Idioms (Go 1.21 - 1.26+)
- Built-ins: Use `max(a, b)` and `min(a, b)` instead of manual if-else blocks.
- Slices/Maps: Use `slices.Contains()`, `slices.Sort()`, and `maps.Copy()` instead of writing loops manually.
- Value Pointers (Go 1.26+): Use `new(42)` or `new(true)` to instantly get a pointer to an inline value.
- Looping: Prefer for i := range n for fixed-count loops instead of old C-style loops.

## Code style
- Import groups: stdlib then third-party, separated by blank line

## Code quality
- Documentation: every function and struct (exported or not) carries a doc comment starting with
  its name and stating the contract/invariants — never a restatement of the signature. Non-obvious
  fields of shared structs get trailing comments. Comments explain *why*, code explains *what*.
- No duplication: extract repeated logic into a named function/method; if two types are structurally
  identical, unify them or alias one. Test scaffolding is table-driven, not copy-pasted.
- Short, single-purpose functions: guard clauses / early returns over deep nesting; no nested
  anonymous functions (extract a named func or method) and no long inline pipelines. Cyclomatic
  complexity budget is enforced by `cyclop` (`make lint`) — split the function instead of raising
  the threshold, unless it is a measured hot path (then note the reason in the report).
- Dead code is deleted in the same change that orphans it: unused functions/types/fields/flags,
  commented-out code, and "for the future" API that no spec requires.

## Concurency and safety
- Goroutines: Never use fire-and-forget goroutines. Always orchestrate via `sync.WaitGroup` or `errgroup.ErrGroup`.
- Channels: Channel size must be exactly 1 or 0 (unbuffered) unless a clear performance justification is provided.
- Context: Always propagate `context.Context` through long-running operations or API tasks for strict cancellation support.

## Resource discipline (memory / allocations)
- Zero-copy reads: traverse large shared structures (>~1 MB) in place under a lock/iterator;
  copy/snapshot APIs are allowed only with a measured justification why direct access is impossible.
- Every new design on a memory/perf task carries an allocation budget: a bytes × entries ×
  workers formula for each added structure; O(workers×data) copies where a lock-only walk
  exists are rejected in review.
- A reference baseline with numbers means reading its code for the *mechanism* of the
  responsible path before designing the fix — matching numbers is not enough.

## 5. Error Handling & Typing
- Explicit Errors: Check `if err != nil` immediately. Do not ignore errors using blank identifier _.
- Error Matching (Go 1.26+): Use `errors.AsType[T](err)` for generic, type-safe error matching.
- Type Assertions: Always use the comma-ok idiom (v, ok := x.(T)) to prevent panics.

## Testing

- Use `testify.Assert*` for assertions
- Prefer table tests to individual tests when possible

## Project Structure

- **main.go** – Entry point, CLI flags: `-size` (5–8), `-workers`, `-precompute-depth`
  (default per board size via `counter.DefaultPrecomputeDepth`; validated `[1, size²/2]`),
  `-gc-percent` (GOGC for the counting pipeline duration, default
  `counter.DefaultGCPercentReversal`, 0 = leave runtime GC untouched)
- **graph/** – `Graph` struct with precomputed knight moves on an N×N board
  - Neighbors in fixed possibleMoves order (no special sorting)
  - Methods: `GetNeighbors()`, `GetDegree()`, `GetNeighborMask()`, `SholdSkip()` (color parity skip for odd boards)
- **state/** – `State` type (uint64 bitboard) tracking visited positions
  - Bit manipulation operations: Visit, Unvisit, IsVisited, CountBits, Intersect, Union, Invert, AllVisited
- **path/** – `Path` value type (state + end); the single key of every table
  (D4-canonical placements in gen A and in the task cache)
- **types/** – Shared `Result` struct (TotalPathsFound, CacheWrites, CacheHits/Misses, Pruned breakdown)
- **searcher/** – DFS over bitmasks with dead-end pruning; no memo tables of its own
  - Methods: `GenerateTasks()` (the single from-start descent into a `cache.Cache` — writes both
    phase A's intermediate table and the task cache), `ExtendTask()` (phase B continuation),
    `CountPathsWithCacheReversal()` (count-DFS with early stop in the task cache);
    no public counting entry points — correctness is pinned by the brute-force oracle
    and the reversal identity tests in searcher_test.go
- **counter/** – High-level counting orchestrator, single pipeline (ADR-016)
  - Methods: `ParallelCount()`, `ParallelCountWithDepth()` (gen A over start groups →
    gen B chunk workers into the task cache → count phase; `total = Σ W(task)·f(task)`)
  - `DefaultPrecomputeDepth(size)` – per-board default split depth
  - `SetGCPercent(p)` – GOGC for the counting pipeline duration (ADR-014); applied on
    entry and restored on exit
- **pruner/** – Stateless necessary-condition pruning (L0 local dead-end + L1 global checks):
  - `Pruner` – `ShouldPruneAfterVisit()` (hot O(deg) check, returns first prune Reason)
- **cache/** – One sharded weight table `Cache` (128 shards, hashed by State only; ADR-018):
  additive `Set()` (`+= w`, zero no-op), concurrent `Get()`, dispatch `Each()` direct-shard walk
  under RLock. Serves both the short-lived gen-A intermediate (materialized to a worklist via
  `Each`, then GC'd) and the task cache (lives until the end of the count phase, never drained).
  Reading is live/`Each` only — no copying Snapshot (it doubled peak memory on large boards).
- **symmetry/** – Exploits board symmetries to reduce search space
  - 8 symmetries: rotations and reflections
  - Methods: `GetCanonicalPosition()`, `GetOrbitSize()`, `GetCanonicalGroups()`, `Canonicalize()`
- **monitoring/** – Progress reporting (`Monitor` interface, `RealMonitor`, `FakeMonitor`)



## Benchmarking

Run benchmarks with:

```bash
make bench
# or directly:
go test -v -bench=. -run=^$ -benchmem ./counter/
# full sweep including the gated 7×7 board (many hours):
make bench-deep
# one board size in its own process (required for meaningful peakRSS — it is a
# per-process maximum): make bench-size N=7 [DEPTHS=20,22]
# gated 8×8 point run (hours/depth): make bench-8x8 DEPTHS=32
# render markdown tables from a benchmark log: make bench-table LOG=bench.log
```

Available benchmarks in `counter/benchmark_test.go`:
- `BenchmarkCountAllTours` – `-precompute-depth` sweep (`size²/2..floor`,
  descending) per board under subtests `size{N}/depth{D}`; publishes per-phase
  FakeMonitor counters as extra metrics (`genA_ms/op`, `genB_ms/op`, `cnt_ms/op`,
  `writesA/op`, `writesB/op`, `prunedA/op`, `prunedB/op`, `cacheHits/op`,
  `cacheMisses/op`) plus memory (`peakRSS_MB/op` — per-process max RSS,
  `totalAllocMB/op` — per-iteration allocation delta)
- sizes 5/6 always run; size 7 is gated by `BENCH_DEEP=1` (`make bench-deep`) and
  stops at depth 6 (`depthFloors`) — below depth 10 measurements take hours, depth 6 OOMs;
  size 8 is gated by `BENCH_8X8=1` (ADR-015). `BENCH_DEPTHS=a,b` overrides the swept depths;
  `BENCH_COUNT_K=n`/`BENCH_COUNT_B=n` override the counting-phase shared-stack depth K and
  claim batch B (plan 13 tuning handles, no CLI flag)
