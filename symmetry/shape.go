package symmetry

import (
	"knighttour/path"
	"knighttour/state"
)

// ShapeCtx holds per-mask normalization work (one PrepareShape call) so every
// end of the same mask can be keyed without redoing the 8 orientation
// transforms. The zero value is ready for PrepareShape.
type ShapeCtx struct {
	shapes [NumTransforms]state.State
	dxs    [NumTransforms]uint8
	dys    [NumTransforms]uint8
}

// PrepareShape fills sc with the normalized shape of st under every orientation:
// each transformed mask is translated so its bbox starts at (0,0); dxs/dys keep
// that translation for end re-encoding in KeyFromPrepared. The D4 group maps
// axis-aligned bboxes exactly onto axis-aligned bboxes, so all eight offsets
// are derived from the original bbox and each orientation needs a single pass.
func (s *Symmetry) PrepareShape(st state.State, sc *ShapeCtx) {
	var pos [maxCells]uint8
	n := 0
	minR, minC := byte(255), byte(255)
	var maxR, maxC uint8
	for p := range st.AllVisited() {
		pos[n] = uint8(p)
		n++
		r, c := s.rows[p], s.cols[p]
		if r < minR {
			minR = r
		}
		if r > maxR {
			maxR = r
		}
		if c < minC {
			minC = c
		}
		if c > maxC {
			maxC = c
		}
	}

	last := byte(s.size - 1)
	// Orientation order matches GetSymmetries; entries give the (min row, min
	// col) of that orientation's transformed bbox.
	orR := [NumTransforms]uint8{minR, minC, last - maxR, last - maxC, minR, last - maxR, minC, last - maxC}
	orC := [NumTransforms]uint8{minC, last - maxR, last - maxC, minR, last - maxC, minC, minR, last - maxR}

	for t := range sc.shapes {
		rOff, cOff := orR[t], orC[t]
		var shape state.State
		for _, p := range pos[:n] {
			tp := s.perms[t][p]
			shape = shape.Visit(int(s.rows[tp]-rOff)*s.size + int(s.cols[tp]-cOff))
		}
		sc.shapes[t], sc.dxs[t], sc.dys[t] = shape, rOff, cOff
	}
}

// normEnd re-encodes end under orientation t using the stored bbox offset.
func (s *Symmetry) normEnd(sc *ShapeCtx, t, end int) uint8 {
	p := s.perms[t][end]
	r, c := s.rows[p]-sc.dxs[t], s.cols[p]-sc.dys[t]
	return uint8(int(r)*s.size + int(c))
}

// KeyFromPrepared returns the canonical key of the (mask, end) class under
// D4 ⋉ translations for an already prepared mask and a given end: the
// lexicographic minimum over orientations; ties on shape are broken by end.
// Values that depend only on the induced subgraph of the mask with a marked
// endpoint (h) are invariant under this group — knight adjacency depends on
// coordinate differences alone and paths never leave the mask, so board edges
// cannot influence them (specs/shapecount.md). The key reuses path.Path:
// normalized mask + endpoint in normalized coordinates.
// The caller must guarantee end ∈ st (as passed to PrepareShape).
func (s *Symmetry) KeyFromPrepared(sc *ShapeCtx, end int) path.Path {
	best, bestEnd := 0, s.normEnd(sc, 0, end)
	for t := 1; t < NumTransforms; t++ {
		e := s.normEnd(sc, t, end)
		if sc.shapes[t] < sc.shapes[best] || (sc.shapes[t] == sc.shapes[best] && e < bestEnd) {
			best, bestEnd = t, e
		}
	}
	return path.New(sc.shapes[best], int(bestEnd))
}

// CanonicalizeShape is PrepareShape + KeyFromPrepared in one call. Prefer the
// prepared context when several ends share a mask (hot paths).
func (s *Symmetry) CanonicalizeShape(st state.State, end int) path.Path {
	var sc ShapeCtx
	s.PrepareShape(st, &sc)
	return s.KeyFromPrepared(&sc, end)
}
