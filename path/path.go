package path

import (
	"fmt"

	"knighttour/state"
)

// Path is a search task: visited cells mask plus current cell.
// It intentionally has no start field: the number of completions depends
// only on (state, end), and orbit contributions are carried as cache weight.
type Path struct {
	state state.State
	end   uint8
}

func New(st state.State, end int) Path {
	return Path{state: st, end: uint8(end)}
}

func (p Path) State() state.State {
	return p.state
}

func (p Path) End() int {
	return int(p.end)
}

func (p Path) String() string {
	return fmt.Sprintf("[-> %d (%s)]", p.end, p.state.String())
}
