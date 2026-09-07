package navigation

import (
	"encoding/json"
	"errors"
)

// ScreenState captures the identity and parameter state of a specific screen in the stack.
type ScreenState struct {
	ScreenID string            `json:"s"`
	Params   map[string]string `json:"p,omitempty"`
}

// Navigator maintains a hierarchical navigation stack for back-tracking and stateful menus.
// This is the interaction-layer navigator; UI only provides button helpers.
// Stack is private to enforce invariants via Push/Pop/Reset.
type Navigator struct {
	stack []ScreenState
}

func (n *Navigator) MarshalJSON() ([]byte, error) {
	type raw struct {
		Stack []ScreenState `json:"st"`
	}
	return json.Marshal(raw{Stack: n.stack})
}

func (n *Navigator) UnmarshalJSON(data []byte) error {
	type raw struct {
		Stack []ScreenState `json:"st"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	n.stack = r.Stack
	return nil
}

// NewNavigator instantiates a Navigator rooted at the specified screen.
func NewNavigator(rootScreen string, params map[string]string) *Navigator {
	if params == nil {
		params = make(map[string]string)
	}
	return &Navigator{
		stack: []ScreenState{
			{ScreenID: rootScreen, Params: params},
		},
	}
}

// Push adds a new screen to the top of the stack.
func (n *Navigator) Push(screenID string, params map[string]string) {
	if params == nil {
		params = make(map[string]string)
	}
	n.stack = append(n.stack, ScreenState{
		ScreenID: screenID,
		Params:   params,
	})
}

// Pop removes and returns the top screen from the stack.
func (n *Navigator) Pop() (ScreenState, bool) {
	if len(n.stack) <= 1 {
		if len(n.stack) == 1 {
			return n.stack[0], false
		}
		return ScreenState{}, false
	}
	top := n.stack[len(n.stack)-1]
	n.stack = n.stack[:len(n.stack)-1]
	return top, true
}

// Current returns the screen at the top of the stack.
func (n *Navigator) Current() ScreenState {
	if len(n.stack) == 0 {
		return ScreenState{}
	}
	return n.stack[len(n.stack)-1]
}

// Root returns the baseline screen at index 0.
func (n *Navigator) Root() ScreenState {
	if len(n.stack) == 0 {
		return ScreenState{}
	}
	return n.stack[0]
}

// Reset clears the stack and sets a new root screen.
func (n *Navigator) Reset(rootScreen string, params map[string]string) {
	if params == nil {
		params = make(map[string]string)
	}
	n.stack = []ScreenState{
		{ScreenID: rootScreen, Params: params},
	}
}

// CanPop indicates whether there is a previous screen to return to.
func (n *Navigator) CanPop() bool {
	return len(n.stack) > 1
}

// Depth returns the number of screens in the stack.
func (n *Navigator) Depth() int {
	return len(n.stack)
}

// Param retrieves a parameter value from the current screen state.
func (n *Navigator) Param(key string) string {
	curr := n.Current()
	if curr.Params == nil {
		return ""
	}
	return curr.Params[key]
}

// SetParam updates or adds a parameter on the current screen state.
func (n *Navigator) SetParam(key, val string) {
	if len(n.stack) == 0 {
		return
	}
	idx := len(n.stack) - 1
	if n.stack[idx].Params == nil {
		n.stack[idx].Params = make(map[string]string)
	}
	n.stack[idx].Params[key] = val
}

// Serialize serializes the navigator to JSON bytes.
func (n *Navigator) Serialize() ([]byte, error) {
	return json.Marshal(n)
}

// DeserializeNavigator parses JSON bytes back into a Navigator.
func DeserializeNavigator(data []byte) (*Navigator, error) {
	var nav Navigator
	if err := json.Unmarshal(data, &nav); err != nil {
		return nil, err
	}
	if len(nav.stack) == 0 {
		return nil, errors.New("empty navigator stack")
	}
	return &nav, nil
}
