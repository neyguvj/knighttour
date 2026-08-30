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
- **graph/** – `Graph` struct with precomputed knight moves on an N×N board
  - Neighbors in fixed possibleMoves order (no special sorting)
  - Methods: `GetNeighbors()`, `GetDegree()`, `GetNeighborMask()`, `SholdSkip()` (color parity skip for odd boards)
- **state/** – `State` type (uint64 bitboard) tracking visited positions
  - Bit manipulation operations: Visit, Unvisit, IsVisited, CountBits, Intersect, Union, Invert, AllVisited
- **path/** – `Path` value type (state + start + end) used as cache key
- **types/** – Shared `Result` struct (TotalPathsFound, CachedPaths)
- **searcher/** – DFS path counter over bitmasks with dead-end pruning
  - Methods: `CountPaths()`, `CountPathsDFS()`, `GenerateSubtasks()` (prefix generation into cache)
- **counter/** – High-level counting orchestrator with symmetry reduction
  - Methods: `ParallelCount()`, `ParallelCountWithDepth()` (multi-worker), `CountFromPosition()`
  - Uses canonical positions to avoid duplicate computation
- **pruner/** – Pruning strategies:
  - `DeadEndPruner` – `ShouldPruneAfterVisit()` (hot O(deg) check)
- **cache/** – Thread-safe memoization with sharded lock implementation (64 shards)
  - Methods: `Get()`, `Set()`, `Clear()`, `ItemsCount()`, `Each()` for caching subtree counts
- **symmetry/** – Exploits board symmetries to reduce search space
  - 8 symmetries: rotations and reflections
  - Methods: `GetCanonicalPosition()`, `GetOrbitSize()`, `GetCanonicalGroups()`, `CanonicalizePath()`
- **monitoring/** – Progress reporting (`Monitor` interface, `RealMonitor`, `FakeMonitor`)



## Benchmarking

Run benchmarks with:

```bash
make bench
# or directly:
go test -v -bench=. -run=^$ -benchmem ./counter/
```

Available benchmarks in `counter/benchmark_test.go`:
- `BenchmarkCountAllToursParallel` – Measures parallel tour counting performance