package halfprof

// bucket.go — Chernov's multi-pass bucketing, the emergency path for heavy
// groups (plan 30 component 5; in-memory 7×7 exceeds the resident threshold,
// ADR-046). A group is counted over `passes` independent passes; pass p keeps
// only the profiles whose seam defect mask falls in bucket p, so each pass
// aggregates about 1/passes of the profiles and the ceiling applies to one
// pass's two tables. The sum of the partial per-pass totals over a fixed pass
// order reproduces the single-pass count bit-for-bit.

// splitmix64 finalizer constants (the odd golden-ratio increment and its two
// mixing multipliers); they give a stable, cheap avalanche on a word.
const (
	splitmixGamma = 0x9E3779B97F4A7C15
	splitmixMul1  = 0xBF58476D1CE4E5B9
	splitmixMul2  = 0x94D049BB133111EB
)

// maskBucket maps a seam defect mask to its bucket in [0, passes) through the
// splitmix64 finalizer. It is a pure function of the mask word — identical in
// every pass, process and build — so both halves of any join pair (which share
// the mask but not their pairing bytes) land in the same bucket and each tour
// is counted exactly once. passes ≤ 1 puts everything in bucket 0.
func maskBucket(k uint64, passes int) int {
	if passes <= 1 {
		return 0
	}
	h := k + splitmixGamma
	h ^= h >> 30
	h *= splitmixMul1
	h ^= h >> 27
	h *= splitmixMul2
	h ^= h >> 31
	return int(h % uint64(passes))
}
