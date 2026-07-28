//go:build windows

package feedback

import (
	"errors"
	"fmt"
	"os"
)

func openDiagnosticRecord(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoRecord
	}
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidRecord
	}
	file, err := os.Open(path) // #nosec G304 -- explicit private diagnostic path.
	if err != nil {
		return nil, fmt.Errorf("%w: open record", ErrInvalidRecord)
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errors.Join(ErrInvalidRecord, err, file.Close())
	}
	return file, nil
}
