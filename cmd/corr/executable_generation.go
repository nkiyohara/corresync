package main

import (
	"errors"
	"os"
)

// Keep the original path and file identity: os.Executable can start naming the
// rollback copy after an atomic update. Each config-scoped owner watches itself.
type executableGeneration struct {
	path string
	info os.FileInfo
}

func captureExecutableGeneration(path string) executableGeneration {
	info, _ := os.Stat(path)
	return executableGeneration{path: path, info: info}
}

func (generation executableGeneration) changed() bool {
	if generation.info == nil {
		return false
	}
	current, err := os.Stat(generation.path)
	// A brief rename gap is not enough to retire; the next poll sees the target.
	if err != nil {
		return false
	}
	return !os.SameFile(generation.info, current) || generation.info.Size() != current.Size() || !generation.info.ModTime().Equal(current.ModTime())
}

func (app *runtime) requireCurrentExecutable() error {
	if app.executableGeneration.changed() {
		return errors.New("corr was updated; restart this command with the current executable")
	}
	return nil
}
