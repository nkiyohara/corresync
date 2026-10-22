package application

import (
	"errors"
	"unicode/utf8"
)

const GraphicalLoginWidth = 1280
const GraphicalLoginHeight = 900
const MaxGraphicalLoginFrameBytes = 2 << 20

// GraphicalLoginAction is ephemeral human input, never an agent browser API.
// No arbitrary URL, script, selector, storage, or credential extraction exists.
type GraphicalLoginAction struct {
	Type  string `json:"type"`
	X     int    `json:"x,omitempty"`
	Y     int    `json:"y,omitempty"`
	Delta int    `json:"delta,omitempty"`
	Text  string `json:"text,omitempty"`
	Key   string `json:"key,omitempty"`
	Shift bool   `json:"shift,omitempty"`
}

func (action GraphicalLoginAction) Validate() error {
	invalid := errors.New("invalid graphical login action")
	if action.X < 0 || action.X >= GraphicalLoginWidth || action.Y < 0 || action.Y >= GraphicalLoginHeight {
		return invalid
	}
	switch action.Type {
	case "refresh", "cancel":
		if action.X != 0 || action.Y != 0 || action.Delta != 0 || action.Text != "" || action.Key != "" || action.Shift {
			return invalid
		}
	case "click", "scroll":
		if action.Text != "" || action.Key != "" || action.Shift || action.Delta < -1000 || action.Delta > 1000 || action.Type == "click" && action.Delta != 0 {
			return invalid
		}
	case "text":
		if action.Text == "" || len(action.Text) > 4096 || !utf8.ValidString(action.Text) || action.Key != "" || action.X != 0 || action.Y != 0 || action.Delta != 0 || action.Shift {
			return invalid
		}
	case "key":
		if action.Text != "" || action.X != 0 || action.Y != 0 || action.Delta != 0 {
			return invalid
		}
		switch action.Key {
		case "Enter", "Tab", "Backspace", "Delete", "Escape", "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End", "PageUp", "PageDown", "SelectAll":
		default:
			return invalid
		}
	default:
		return invalid
	}
	return nil
}

// GraphicalLoginFrame is a bounded viewport; it is never recorded or audited.
// Origin excludes paths, queries, and fragments from identity-provider URLs.
type GraphicalLoginFrame struct {
	JPEG   []byte `json:"jpeg"`
	Origin string `json:"origin"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
