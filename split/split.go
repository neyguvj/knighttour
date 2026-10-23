// Package split counts Hamiltonian paths and cycles of an N×N knight board by a
// balanced vertical cut in half (plan 26): the halves are processed by independent
// layer-cake frontier DP down to interface states σ = (S, M, ε) — used seam edges,
// their stub pairing by path fragments inside the half, and the number of global
// ends — and the result is Σ N_L(σ)·N_R(σ̄) over compatible states with connectivity
// checked by composing the pairings. The package is a standalone census instrument
// outside the CLI and the reversal pipeline (the role precedent is zdd, ADR-040).
//
// Counting convention: the engine enumerates edge sets, each tour once regardless
// of direction; comparison with the directed references of docs/requirements.md is
// by multiplying both families by 2 (convention of plans 07 and 23).
//
// The count is single-threaded and deterministic: fixed inputs reproduce the total
// and every Census counter bit-exactly across runs and processes; only wall time
// floats. Memory is bounded by a soft byte ceiling inside the package: exceeding
// it returns an OverCapError with progress counters instead of pressing the OOM
// boundary (mechanic of ADR-038/040). There are no environment or flag handles
// (ADR-020); tests move the internal lever directly.
package split

import (
	"fmt"
	"time"
)

// Family selects the counted Hamiltonian family.
type Family int

const (
	// Paths counts Hamiltonian paths: degrees ≤ 2, connected, exactly two degree-1 vertices.
	Paths Family = iota
	// Cycles counts Hamiltonian cycles: every vertex has degree 2.
	Cycles
)

// Variant is the seam geometry variant compared by the census.
type Variant int

const (
	// Straight is the balanced vertical cut c = ⌈n/2⌉.
	Straight Variant = iota
	// Mirror is the same cut with D4 compression: one table serves both halves; even n only.
	Mirror
)

// Bounds of the contract: the applicability range is 5..8, but the core stays
// correct below it and is pinned there by oracle tests.
const (
	minSize = 2
	maxSize = 8
)

// Census is a run metrics snapshot: SigmaL/SigmaR are the numbers of reachable
// interface states σ per half; LayerPeak is the maximum of live DP states over
// layers and halves; Joins is the number of same-S pairs checked at gluing;
// MaxWeight is the largest state weight seen during the build.
type Census struct {
	SigmaL, SigmaR int
	LayerPeak      int
	Joins          uint64
	MaxWeight      uint64
}

// runTimes separates the two measured phases of a count (census metrics dp_ms
// and join_ms); it stays unexported — the public Census carries no timings.
type runTimes struct {
	dp, join time.Duration
}

// Count counts the tours of an n×n board by the balanced vertical cut with the
// straight variant. Invalid input (unknown family, unusable n) is an error; a
// build over the soft byte ceiling returns OverCapError, never OOM.
func Count(n int, fam Family) (uint64, error) {
	total, _, err := CountWithCensus(n, fam, Straight)
	return total, err
}

// CountWithCensus is the same count with an explicit cut variant and a census snapshot.
func CountWithCensus(n int, fam Family, v Variant) (uint64, Census, error) {
	total, cen, _, err := countInternal(n, fam, v)
	return total, cen, err
}

// countInternal runs the pipeline and additionally reports the phase durations.
func countInternal(n int, fam Family, v Variant) (uint64, Census, runTimes, error) {
	if err := validate(n, fam, v); err != nil {
		return 0, Census{}, runTimes{}, err
	}
	g := newCutGeom(n)
	kl, ok := newKeyLayout(g)
	if !ok { // unreachable for validated n: the layout bounds are the contract's
		return 0, Census{}, runTimes{}, fmt.Errorf("split: board %d exceeds key layout bounds", n)
	}
	e, err := newEngine(&kl, fam)
	if err != nil {
		return 0, Census{}, runTimes{}, err
	}
	dpStart := time.Now()
	sigL, err := e.runHalf(&g.left)
	if err != nil {
		return 0, Census{}, runTimes{}, err
	}
	recL, arena := e.collect(sigL, nil, nil)
	var recR []sigRec
	if v == Mirror {
		recR, arena = e.collect(sigL, g.mirror, arena)
	} else {
		if err := e.retain(sigL); err != nil {
			return 0, Census{}, runTimes{}, err
		}
		sigR, err := e.runHalf(&g.right)
		if err != nil {
			return 0, Census{}, runTimes{}, err
		}
		recR, arena = e.collect(sigR, nil, arena)
	}
	dpDur := time.Since(dpStart)
	joinStart := time.Now()
	total, joins := e.glue(recL, recR, arena)
	cen := Census{
		SigmaL:    len(recL),
		SigmaR:    len(recR),
		LayerPeak: e.layerPeak,
		Joins:     joins,
		MaxWeight: e.maxWeight,
	}
	return total, cen, runTimes{dp: dpDur, join: time.Since(joinStart)}, nil
}

// validate rejects unknown families/variants, boards outside [minSize,maxSize]
// and the mirror variant on odd n (halves of unequal width, no reflection symmetry).
func validate(n int, fam Family, v Variant) error {
	if n < minSize || n > maxSize {
		return fmt.Errorf("split: board size %d outside [%d,%d]", n, minSize, maxSize)
	}
	if fam != Paths && fam != Cycles {
		return fmt.Errorf("split: unknown family %d", int(fam))
	}
	if v != Straight && v != Mirror {
		return fmt.Errorf("split: unknown variant %d", int(v))
	}
	if v == Mirror && n%2 != 0 {
		return fmt.Errorf("split: mirror variant needs an even board, got %d", n)
	}
	return nil
}
