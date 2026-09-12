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
- `/feature` – full pipeline: interview (skills) → specs/plan updated → **coder ⇄ reviewer loop**
  until `VERDICT: APPROVED` (max 5 iters, sessions resumed by task_id) → `benchmarker` writes ADR
  measurements for hot-path changes. Subagents: `coder` (implements to green `make check`),
  `reviewer` (read-only, severity BLOCKER/MAJOR/MINOR + `SPEC_OK`), `benchmarker`
  (WIN/REGRESSION/NOISE, records numbers in ADR). Restart opencode after editing agent/skill files.

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
  `-tail-memo` (counting tail memo threshold K, 0 = off), `-mode` (`class|reversal`, default
  `class`)
- **graph/** – `Graph` struct with precomputed knight moves on an N×N board
  - Neighbors in fixed possibleMoves order (no special sorting)
  - Methods: `GetNeighbors()`, `GetDegree()`, `GetNeighborMask()`, `SholdSkip()` (color parity skip for odd boards)
- **state/** – `State` type (uint64 bitboard) tracking visited positions
  - Bit manipulation operations: Visit, Unvisit, IsVisited, CountBits, Intersect, Union, Invert, AllVisited
- **path/** – `Path` value type (state + end); the single key of every accumulator
  (D4-canonical placements in gen A, shape classes in M)
- **types/** – Shared `Result` struct (TotalPathsFound, CacheWrites, Pruned breakdown)
- **searcher/** – DFS over bitmasks with dead-end pruning; no memo tables of its own
  - Methods: `GenerateRoots()` (phase A prefix emission into a LocalSink),
    `ExtendToClasses()` (phase B complement-class emission into the M accumulator);
    reversal mode: `GenerateTasks()`/`ExtendTask()` (task-cache generation),
    `CountPathsWithCacheReversal()` (count-DFS with early stop in the task cache);
    no public counting entry points — correctness is pinned by the brute-force
    and shallow phase-B oracles in searcher_test.go
- **counter/** – High-level counting orchestrator, two modes behind `SetMode`
  - Methods: `ParallelCount()`, `ParallelCountWithDepth()` (gen A over start groups →
    gen B chunk workers → final pass; class: `total = Σ h(C)·M(C)`, reversal:
    `total = Σ W(task)·f(task)` via task-cache); `ModeClass` default, `ModeReversal`
  - `DefaultPrecomputeDepth(size)` – per-board default split depth
- **pruner/** – Pruning strategies:
  - `DeadEndPruner` – `ShouldPruneAfterVisit()` (hot O(deg) check)
- **cache/** – Two sharded weight tables (128 shards, hashed by State only):
  - `Accumulator` keyed by `path.Path` (`Add()`, `DrainShard(i)`, `Drain()`, `ItemsCount()`);
    writers use `Local()` → `LocalSink` (per-goroutine buffer, threshold `Flush`) to avoid
    lock churn. Reading is per-shard drain (no copying Snapshot — it doubled peak memory).
  - `Cache` – reversal-mode task cache (`Set()`, concurrent `Get()`, lazy `NumShards()`/
    `SnapshotShard(i)` dispatch); lives until the end of the count phase, never drained per shard.
- **shapecount/** – DP h(shape,ends) per translation+D4 shape class, no memo table (class mode final pass)
  - Methods: `CountShape(shape, ends)` (shared memo across ends of one shape),
    `CountShapeWithTail(..., tail)` + `NewTail()`/`SetTailMemo(k, slots)` – optional
    persistent per-worker tail memo f(cur,todo), popcount(todo) ≤ K (plan 03 variant B)
- **symmetry/** – Exploits board symmetries to reduce search space
  - 8 symmetries: rotations and reflections
  - Methods: `GetCanonicalPosition()`, `GetOrbitSize()`, `GetCanonicalGroups()`, `Canonicalize()`
  - Shape classes (D4 + translations): `CanonicalizeShape()`, `PrepareShape()`/`KeyFromPrepared()`
    returning `path.Path` (the separate `ShapeKey` type was removed)
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
- `BenchmarkCountAllToursClass` – `-precompute-depth` sweep (`size²/2..floor`,
  descending) per board under subtests `size{N}/depth{D}`; publishes per-phase
  FakeMonitor counters as extra metrics (`genA_ms/op`, `genB_ms/op`, `cnt_ms/op`,
  `writesA/op`, `writesB/op`, `prunedA/op`, `prunedB/op`, `classes/op`,
  `shapes/op`, `zeros/op`) plus memory (`peakRSS_MB/op` — per-process max RSS,
  `totalAllocMB/op` — per-iteration allocation delta)
- sizes 5/6 always run; size 7 is gated by `BENCH_DEEP=1` (`make bench-deep`) and
  stops at depth 6 (`depthFloors`) — below depth 10 measurements take hours, depth 6 OOMs;
  size 8 is gated by `BENCH_8X8=1`. `BENCH_DEPTHS=a,b` overrides the swept depths
