//go:build windows

package localipc

import (
	"context"
	"errors"
)

// StopEndpointOwner is unavailable on Windows because named-pipe endpoints do
// not vary with Unix runtime environment and therefore cannot split this way.
func StopEndpointOwner(context.Context, Endpoint) (int, error) {
	return 0, errors.New("split Unix session-owner recovery is unavailable on Windows")
}
