package main

import (
	runtimepkg "runtime"
	"strings"
)

// Printed commands target a POSIX shell on Unix and PowerShell on Windows.
// Execution always uses the original typed argv, never this presentation text.
func formatCommand(name string, arguments []string) string {
	return formatCommandForOS(runtimepkg.GOOS, name, arguments)
}

func formatCommandForOS(goos, name string, arguments []string) string {
	parts := make([]string, 0, len(arguments)+2)
	if goos == "windows" {
		// PowerShell requires its call operator for a quoted executable path.
		parts = append(parts, "&")
	}
	parts = append(parts, quoteCommandArgumentForOS(goos, name))
	for _, argument := range arguments {
		parts = append(parts, quoteCommandArgumentForOS(goos, argument))
	}
	return strings.Join(parts, " ")
}

func quoteCommandArgumentForOS(goos, value string) string {
	if goos == "windows" {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:@%+=,-", r)
		return !safe
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
