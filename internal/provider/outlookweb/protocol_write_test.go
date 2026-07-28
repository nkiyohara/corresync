package outlookweb

import (
	"errors"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

func TestCheckWriteResponseClassifiesOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		class       string
		code        string
		wantError   bool
		wantUnknown bool
	}{
		{name: "success", class: "Success", code: "NoError"},
		{name: "explicit failure", class: "Error", code: "ErrorItemNotFound", wantError: true},
		{name: "warning", class: "Warning", code: "ErrorBatchProcessingStopped", wantError: true, wantUnknown: true},
		{name: "contradictory success", class: "Success", code: "ErrorInternalServerError", wantError: true, wantUnknown: true},
		{name: "missing fields", wantError: true, wantUnknown: true},
		{name: "invalid code", class: "Error", code: "unsafe response", wantError: true, wantUnknown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := checkWriteResponse(CreateItem, test.class, test.code)
			if (err != nil) != test.wantError {
				t.Fatalf("checkWriteResponse() error = %v, wantError %t", err, test.wantError)
			}
			if errors.Is(err, application.ErrWriteOutcomeUnknown) != test.wantUnknown {
				t.Fatalf("checkWriteResponse() error = %v, wantUnknown %t", err, test.wantUnknown)
			}
		})
	}
}
