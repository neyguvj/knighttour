package path

import (
	"fmt"
	"knighttour/state"
)

type Path struct {
	state state.State
	start uint8
	end   uint8
}

func New(state state.State, start int, end int) Path {
	return Path{state: state, start: uint8(start), end: uint8(end)}
}

func (p Path) State() state.State {
	return p.state
}

func (p Path) Start() int {
	return int(p.start)
}

func (p Path) End() int {
	return int(p.end)
}

func (p Path) String() string {
	return fmt.Sprintf("[%d -> %d (%s)]", p.start, p.end, p.state.String())
}
