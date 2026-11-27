package main

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func windowsSignInDialogPath() string {
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return ""
	}
	return filepath.Join(directory, "WindowsPowerShell", "v1.0", "powershell.exe")
}
