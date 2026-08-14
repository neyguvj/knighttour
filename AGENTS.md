# AGENTS.md

## Quick Start

```bash
go run main.go        # Build & run
go test ./...         # Run tests (none currently)
go vet ./...          # Static analysis
go fmt ./...          # Format code
```

## Chat settings
- Answer in Russian language.

## Environment & Version Awareness
- Detect Version: Read go.mod before writing code. 
- Target Version: Assume Go 1.26+ unless stated otherwise in go.mod.

## Workflow
- Before code modification
  1. read specs for given `specs/` folder
  2. create it if it does not exist
  3. update specs before code modification
  4. Use updated specs to make changes and tests for new code
- Post-Change Verification: After every code modification, you MUST run:
  1. `go fmt ./...`
  2. `go vet ./...`
  3. `go test -race ./...` (strictly require race detector)
  4. `go fix ./...` (to modernize idioms automatically)  

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

- **main.go** – Entry point, creates a knight tour graph (5×5 to 8×8), supports parallel search
- **graph/** – `Graph` struct with precomputed knight moves on an N×N board
  - Edges sorted by degree (highest first) for efficient traversal
  - Methods: `CountNeighbors()`, `IsConnected()` for connectivity checks
- **state/** – `State` type (uint64 bitboard) tracking visited positions
  - Bit manipulation operations: Visit, Unvisit, IsVisited, CountBits, Intersect, Union
- **searcher/** – DFS path counter with Warnsdorff ordering and pruning
  - Methods: `CountPaths()`, `CountFromState()` for recursive traversal
    - `startPos` parameter added to `countDFS` chain for cache canonicalization
- **counter/** – High-level counting orchestrator with symmetry reduction
  - Methods: `CountAllTours()` (sequential), `ParallelCount()` (multi-worker)
  - Uses canonical positions to avoid duplicate computation
- **pruner/** – Pruning strategies:
  - `DeadEndPruner` – Detects unreachable squares, prunes when unvisited isolated nodes exist
- **cache/** – Thread-safe memoization with sharded lock implementation (64 shards)
  - Methods: `Get()`, `Set()`, `Clear()` for caching subtree counts
- **symmetry/** – Exploits board symmetries to reduce search space
  - 8 symmetries: rotations and reflections
  - Methods: `GetCanonicalPosition()`, `GetOrbitSize()`, `GetCanonicalGroups()`



## Benchmarking

Run benchmarks with:

```bash
go test -v -bench=. -run=^$ -benchmem ./counter/
```

Available benchmarks in `counter/benchmark_test.go`:
- `BenchmarkCountAllToursSequential` – Measures serial tour counting performance
- `BenchmarkCountAllToursParallel` – Measures parallel tour counting (8 workers)