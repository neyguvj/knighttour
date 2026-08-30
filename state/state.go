package state

import (
	"fmt"
	"iter"
	"math/bits"
)

type State uint64

func Bit(pos int) State {
	return 1 << uint64(pos)
}

func NewState(visited ...int) State {
	state := State(0)
	for _, v := range visited {
		state = state.Visit(v)
	}
	return state
}

func (s State) IsVisited(pos int) bool {
	return (s & (1 << uint64(pos))) != 0
}

func (s State) Visit(pos int) State {
	return s | (1 << uint64(pos))
}

func (s State) Unvisit(pos int) State {
	return s &^ (1 << uint64(pos))
}

func (s State) IsFull(cellsCount int) bool {
	fullMask := State((1 << cellsCount) - 1)
	return s == fullMask
}

func (s State) CountBits() int {
	return bits.OnesCount64(uint64(s))
}

func (s State) IsUnvisited(pos int) bool {
	return !s.IsVisited(pos)
}

func (s State) GetUnvisitedMask(cellsCount int) State {
	boardMask := State((1 << cellsCount) - 1)
	return boardMask & ^s
}

func (s State) UnvisitedMask(cellsCount int) State {
	return s.GetUnvisitedMask(cellsCount)
}

func (s State) Intersect(mask State) State {
	return s & mask
}

func (s State) ShiftLeft(n int) State {
	return s << uint64(n)
}

func (s State) IsBitSet(pos int) bool {
	return s.IsVisited(pos)
}

func (s State) Union(mask State) State {
	return s | mask
}

func (s State) ShiftRight(n int) State {
	return s >> uint64(n)
}

func (s State) IsEmpty() bool {
	return s == 0
}

func (s State) TrailingZeroBits() uint {
	return uint(bits.TrailingZeros64(uint64(s)))
}

func (s State) AllVisited() iter.Seq[int] {
	return func(yield func(int) bool) {
		for m := uint64(s); m != 0; m &= m - 1 {
			if !yield(bits.TrailingZeros64(m)) {
				return
			}
		}
	}
}

func (s State) AndNot(mask State) State {
	return s & ^mask
}

func (s State) Invert(cellsCount int) State {
	fullMask := State((1 << cellsCount) - 1)
	return fullMask & ^s
}

func (s State) IsHalfway(cellsCount int) bool {
	if cellsCount%2 == 0 {
		return s.CountBits() <= cellsCount/2
	}
	return s.CountBits() <= (cellsCount+1)/2
}

func (s State) HalfwayPoint(cellsCount int) int {
	if cellsCount%2 == 0 {
		return cellsCount / 2
	}
	bits := s.CountBits()
	if bits == (cellsCount-1)/2 {
		return (cellsCount - 1) / 2
	}
	if bits == (cellsCount+1)/2 {
		return (cellsCount + 1) / 2
	}
	return -1
}

func (s State) String() string {
	return fmt.Sprintf("%b", s)
}
