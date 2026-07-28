//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func startDetachedDaemon(ctx context.Context, configPath string) (returnErr error) {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve Corresync executable: %w", err)
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0) // #nosec G304 -- platform null device.
	if err != nil {
		return fmt.Errorf("open null device: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, null.Close())
	}()
	command := exec.CommandContext(context.WithoutCancel(ctx), executable, "--config", configPath, "daemon", "serve") // #nosec G204 -- self executable and validated path.
	command.Env = os.Environ()
	command.Stdin, command.Stdout, command.Stderr = null, null, null
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
		HideWindow:    true,
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start session owner: %w", err)
	}
	if err := command.Process.Release(); err != nil {
		return fmt.Errorf("release session owner process: %w", err)
	}
	return nil
}
