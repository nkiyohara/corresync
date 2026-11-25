package application

import (
	"strings"
	"testing"
)

func TestGraphicalLoginActionValidate(t *testing.T) {
	for _, test := range []struct {
		name   string
		action GraphicalLoginAction
		valid  bool
	}{
		{"refresh", GraphicalLoginAction{Type: "refresh"}, true},
		{"move", GraphicalLoginAction{Type: "move", X: 10, Y: 20}, true},
		{"move at the last pixel", GraphicalLoginAction{Type: "move", X: GraphicalLoginWidth - 1, Y: GraphicalLoginHeight - 1}, true},
		{"move outside the width", GraphicalLoginAction{Type: "move", X: GraphicalLoginWidth, Y: 1}, false},
		{"move outside the height", GraphicalLoginAction{Type: "move", X: 1, Y: GraphicalLoginHeight}, false},
		{"move negative", GraphicalLoginAction{Type: "move", X: -1, Y: 1}, false},
		{"move with delta", GraphicalLoginAction{Type: "move", X: 1, Y: 1, Delta: 5}, false},
		{"move with text", GraphicalLoginAction{Type: "move", X: 1, Y: 1, Text: "x"}, false},
		{"move with key", GraphicalLoginAction{Type: "move", X: 1, Y: 1, Key: "Enter"}, false},
		{"move with shift", GraphicalLoginAction{Type: "move", X: 1, Y: 1, Shift: true}, false},
		{"click", GraphicalLoginAction{Type: "click", X: 1, Y: 1}, true},
		{"text at the bound", GraphicalLoginAction{Type: "text", Text: strings.Repeat("x", 4096)}, true},
		{"text over the bound", GraphicalLoginAction{Type: "text", Text: strings.Repeat("x", 4097)}, false},
		{"unknown key", GraphicalLoginAction{Type: "key", Key: "F5"}, false},
		{"unknown type", GraphicalLoginAction{Type: "hover", X: 1, Y: 1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.action.Validate(); (err == nil) != test.valid {
				t.Fatalf("Validate() = %v, want valid=%v", err, test.valid)
			}
		})
	}
}
