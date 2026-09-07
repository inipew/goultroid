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
type Navigator struct {
	Stack []ScreenState `json:"st"`
}

// NewNavigator instantiates a Navigator rooted at the specified screen.
func NewNavigator(rootScreen string, params map[string]string) *Navigator {
	if params == nil {
		params = make(map[string]string)
	}
	return &Navigator{
		Stack: []ScreenState{
			{ScreenID: rootScreen, Params: params},
		},
	}
}

// Push adds a new screen to the top of the stack.
func (n *Navigator) Push(screenID string, params map[string]string) {
	if params == nil {
		params = make(map[string]string)
	}
	n.Stack = append(n.Stack, ScreenState{
		ScreenID: screenID,
		Params:   params,
	})
}

// Pop removes and returns the top screen from the stack.
func (n *Navigator) Pop() (ScreenState, bool) {
	if len(n.Stack) <= 1 {
		if len(n.Stack) == 1 {
			return n.Stack[0], false
		}
		return ScreenState{}, false
	}
	top := n.Stack[len(n.Stack)-1]
	n.Stack = n.Stack[:len(n.Stack)-1]
	return top, true
}

// Current returns the screen at the top of the stack.
func (n *Navigator) Current() ScreenState {
	if len(n.Stack) == 0 {
		return ScreenState{}
	}
	return n.Stack[len(n.Stack)-1]
}

// Root returns the baseline screen at index 0.
func (n *Navigator) Root() ScreenState {
	if len(n.Stack) == 0 {
		return ScreenState{}
	}
	return n.Stack[0]
}

// Reset clears the stack and sets a new root screen.
func (n *Navigator) Reset(rootScreen string, params map[string]string) {
	if params == nil {
		params = make(map[string]string)
	}
	n.Stack = []ScreenState{
		{ScreenID: rootScreen, Params: params},
	}
}

// CanPop indicates whether there is a previous screen to return to.
func (n *Navigator) CanPop() bool {
	return len(n.Stack) > 1
}

// Depth returns the number of screens in the stack.
func (n *Navigator) Depth() int {
	return len(n.Stack)
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
	if len(n.Stack) == 0 {
		return
	}
	idx := len(n.Stack) - 1
	if n.Stack[idx].Params == nil {
		n.Stack[idx].Params = make(map[string]string)
	}
	n.Stack[idx].Params[key] = val
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
	if len(nav.Stack) == 0 {
		return nil, errors.New("empty navigator stack")
	}
	return &nav, nil
}
