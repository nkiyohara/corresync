package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	goruntime "runtime"
	"strings"
)

// localSignInReview runs on the daemon desktop. Only a positive response in
// this independent UI can authorize opening a previously unverified route.
type localSignInReview func(context.Context, string) error

func desktopSignInReview() localSignInReview {
	var program string
	var args []string
	switch goruntime.GOOS {
	case "linux":
		program = "/usr/bin/zenity"
		args = []string{"--text-info", "--title=Corresync sign-in", "--checkbox=I recognize these destinations", "--ok-label=Continue", "--cancel-label=Cancel"}
		if _, err := os.Stat("/usr/bin/zenity"); err != nil && os.Getenv("DISPLAY") != "" {
			program = "/usr/bin/xmessage"
			args = []string{"-center", "-title", "Corresync sign-in", "-buttons", "Cancel:1,Continue:73", "-default", "Cancel", "-file", "-"}
		}
	case "darwin":
		program = "/usr/bin/osascript"
		args = []string{"-e", `use framework "Foundation"`, "-e", "use scripting additions",
			"-e", "set inputData to current application's NSFileHandle's fileHandleWithStandardInput()'s readDataToEndOfFile()",
			"-e", "set notice to current application's NSString's alloc()'s initWithData:inputData encoding:4",
			"-e", `display dialog (notice as text) with title "Corresync sign-in" buttons {"Cancel", "Continue"} default button "Cancel" cancel button "Cancel"`}

	case "windows":
		program = windowsSignInDialogPath()
		if program == "" {
			return nil
		}

		args = []string{"-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop'; [Console]::InputEncoding = [Text.Encoding]::UTF8; Add-Type -AssemblyName System.Windows.Forms; if ([Windows.Forms.MessageBox]::Show([Console]::In.ReadToEnd(),'Corresync sign-in','YesNo','Question','Button2') -ne 'Yes') { exit 1 }"}
	default:
		return nil
	}
	if info, err := os.Stat(program); err != nil || !info.Mode().IsRegular() { // #nosec G703 -- program is fixed by the OS-specific implementation.
		return nil
	}
	return func(ctx context.Context, notice string) error {
		cmd := exec.CommandContext(ctx, program, args...) // #nosec G204,G702 -- fixed OS program and arguments.
		cmd.Stdin = strings.NewReader(notice)
		err := cmd.Run()
		if program == "/usr/bin/xmessage" {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 73 && ctx.Err() == nil {
				return nil
			}
			return errors.New("local destination review was not accepted")
		}
		return err
	}
}
