package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	goruntime "runtime"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/loginui"
)

// graphicalWireVersion is the SSH helper protocol. Version 2 adds the pointer
// move action, so a newer local side stops at the first request against an
// older helper instead of ending the login at the first move. The helper
// still answers version 1, which never carries a move, in version 1.
// Unchanged frames are omitted in both; a version 1 local side sees no frame
// and keeps its picture.
const (
	graphicalWireVersion   = 2
	graphicalWireVersionV1 = 1
)
const graphicalLoginLifetime = 10 * time.Minute

type graphicalWireRequest struct {
	Version int                               `json:"version"`
	Action  *application.GraphicalLoginAction `json:"action"`
}
type graphicalWireResponse struct {
	Version int            `json:"version"`
	Result  loginui.Result `json:"result"`
	Failed  bool           `json:"failed,omitempty"`
}

func graphicalExchange(app *runtime, client terminalLoginClient, account domain.AccountID) loginui.Exchange {
	var sessionID string
	started := false
	return func(ctx context.Context, action *application.GraphicalLoginAction) (loginui.Result, error) {
		if started && sessionID == "" {
			return loginui.Result{}, errors.New("login ended or start outcome unknown; start a new command")
		}
		if !started && action != nil {
			return loginui.Result{}, errors.New("login has not started")
		}
		started = true
		result, err := client.TerminalLogin(ctx, daemonapi.TerminalLoginInput{Account: account, SessionID: sessionID, Graphical: true, GraphicalAction: action}, app.caller())
		if err != nil {
			return loginui.Result{}, err
		}
		sessionID = result.SessionID
		return loginui.Result{Status: result.Status, Frame: result.Frame}, nil
	}
}

func runGraphicalLogin(app *runtime, client terminalLoginClient, account domain.AccountID, port int, stdio bool, loginTimeout time.Duration) error {
	lifetime := graphicalLoginLifetime
	if loginTimeout > 0 && loginTimeout < lifetime {
		lifetime = loginTimeout
	}
	ctx, cancel := context.WithTimeout(app.context, lifetime)
	defer cancel()
	exchange := graphicalExchange(app, client, account)
	if stdio {
		// stderr travels back over SSH to the terminal that ran `--host`.
		return serveGraphicalStdio(ctx, app.stdin, app.stdout, reportGraphicalFailures(app, exchange))
	}
	return serveGraphicalUI(ctx, app, exchange, port, "", lifetime)
}

// reportGraphicalFailures writes each new interaction failure to the human
// CLI. The login page only shows fixed guidance; the terminal has the reason.
// A failed start is returned by the command itself and is not repeated here.
func reportGraphicalFailures(app *runtime, exchange loginui.Exchange) loginui.Exchange {
	last := ""
	return func(ctx context.Context, action *application.GraphicalLoginAction) (loginui.Result, error) {
		result, err := exchange(ctx, action)
		switch {
		case err == nil:
			last = ""
		case action != nil && action.Type == "move":
			// A pointer move that did not arrive is dropped by the page; it is
			// not worth a line in the terminal.
		case action != nil && err.Error() != last:
			last = err.Error()
			_, _ = fmt.Fprintf(app.stderr, "login view: %s\n", last)
		}
		return result, err
	}
}

// sshHostGuess suggests this host's name for the printed SSH command. It is
// only a hint: the user's own SSH alias may differ.
func sshHostGuess() string {
	if name, err := os.Hostname(); err == nil && graphicalSSHHostPattern.MatchString(name) {
		return name
	}
	return "SSH_HOST"
}

func formatLoginLifetime(lifetime time.Duration) string {
	if lifetime < time.Minute {
		return fmt.Sprintf("%d seconds", int(lifetime.Round(time.Second)/time.Second))
	}
	if minutes := int(lifetime.Round(time.Minute) / time.Minute); minutes > 1 {
		return fmt.Sprintf("%d minutes", minutes)
	}
	return "1 minute"
}

// serveGraphicalUI serves the login view. sshHost is empty when the view runs
// on the remote host itself and needs a forwarded port; otherwise it names the
// SSH host that owns the browser and the view opens in the local browser.
func serveGraphicalUI(ctx context.Context, app *runtime, exchange loginui.Exchange, port int, sshHost string, lifetime time.Duration) error {
	server, err := loginui.New(ctx, port, reportGraphicalFailures(app, exchange))
	if err != nil {
		return err
	}
	// Print the capability only to the explicitly invoked human CLI, never logs.
	// Each line fits 80 columns except the link and a long host name, which
	// stand on lines of their own so a terminal wraps nothing else with them.
	if sshHost == "" {
		host := sshHostGuess()
		_, err = fmt.Fprintf(app.stdout, `Sign in from a browser on your own computer.

1. On your own computer, start this SSH tunnel and leave it running:

   ssh -N -L 127.0.0.1:%d:127.0.0.1:%d %s

   If you reach this server by another name, use that instead of %s.

2. Open this link in a browser there. It is private; do not share it.

%s

The link expires in %s. Press Ctrl-C here to cancel.
The account session stays on this host.
`, server.Port(), server.Port(), host, host, server.URL(), formatLoginLifetime(lifetime))
	} else {
		_, err = fmt.Fprintf(app.stdout, `Opening the sign-in page in your browser. If it does not open, visit:

%s

The link is private and expires in at most %s.
Press Ctrl-C here to cancel. The account session stays on %s.
`, server.URL(), formatLoginLifetime(lifetime), sshHost)
		if err == nil {
			openGraphicalLoginBrowser(ctx, server.URL())
		}
	}
	if err != nil {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_ = server.Run(cancelled)
		return err
	}
	if err := server.Run(ctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("the login view expired before sign-in completed; run the command again")
		}
		return err
	}
	owner := "this host"
	if sshHost != "" {
		owner = sshHost
	}
	switch server.Outcome() {
	case "authenticated":
		_, err = fmt.Fprintf(app.stdout, "Signed in. The account session stays on %s.\n", owner)
	case "cancelled":
		_, err = fmt.Fprintln(app.stdout, "Login cancelled.")
	}
	return err
}

func openGraphicalLoginBrowser(ctx context.Context, target string) {
	var command *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "open", target) // #nosec G204 -- target is our generated loopback pairing URL, never shell input.
	case "linux":
		command = exec.CommandContext(ctx, "xdg-open", target) // #nosec G204 -- target is our generated loopback pairing URL, never shell input.
	default:
		return // The printed URL remains the portable fallback.
	}
	go func() { _ = command.Run() }()
}

func serveGraphicalStdio(ctx context.Context, reader io.Reader, writer io.Writer, exchange loginui.Exchange) error {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_, _ = exchange(cleanup, &application.GraphicalLoginAction{Type: "cancel"})
	}()
	var sent sentFrame
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 8<<10)
	lines := make(chan []byte)
	readDone := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
		readDone <- scanner.Err()
	}()
	for {
		var line []byte
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readDone:
			return err
		case line = <-lines:
		}

		var request graphicalWireRequest
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Version != graphicalWireVersion && request.Version != graphicalWireVersionV1 {
			return errors.New("incompatible graphical login protocol; update Corresync on both computers")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("invalid graphical login request")
		}
		if request.Action != nil {
			if err := request.Action.Validate(); err != nil {
				return err
			}
			if request.Version == graphicalWireVersionV1 && request.Action.Type == "move" {
				return errors.New("invalid graphical login request")
			}
		}
		result, err := exchange(ctx, request.Action)
		if err == nil {
			result = sent.skipUnchanged(result)
			result.ExpiresIn = remainingSeconds(ctx)
		}
		// Answer in the version that was asked: an older local side still works.
		response := graphicalWireResponse{Version: request.Version, Result: result, Failed: err != nil}
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			return errors.New("login connection closed")
		}
		// A failed start or cancellation ends the helper. Any later failure is
		// reported and the login stays open, as it does for a local view: the
		// local side counts unreadable screens and cancels when it gives up.
		if err != nil && (request.Action == nil || request.Action.Type == "cancel") {
			return err
		}
		if err == nil && (result.Status == "authenticated" || result.Status == "cancelled") {
			return nil
		}
	}
}

// sentFrame remembers a digest of the last frame the helper sent, so an
// unchanged screen is not sent over SSH again. The local side keeps its copy
// of that frame. An older local side, which does not know the unchanged
// flag, sees no frame and keeps its picture as it does while a page loads.
type sentFrame struct {
	known  bool
	digest [sha256.Size]byte
	origin string
}

func (sent *sentFrame) skipUnchanged(result loginui.Result) loginui.Result {
	if result.Frame == nil {
		return result
	}
	digest := sha256.Sum256(result.Frame.JPEG)
	if sent.known && digest == sent.digest && result.Frame.Origin == sent.origin {
		result.Frame = nil
		result.Unchanged = true
		return result
	}
	sent.known, sent.digest, sent.origin = true, digest, result.Frame.Origin
	return result
}

// remainingSeconds reports the helper's remaining lifetime, rounded up, so
// the local page can show when the remote login ends.
func remainingSeconds(ctx context.Context) int {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > graphicalLoginLifetime {
		return 0
	}
	return int((remaining + time.Second - 1) / time.Second)
}

var graphicalSSHHostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]*$`)

func graphicalSSHArguments(host, account string) ([]string, error) {
	if !graphicalSSHHostPattern.MatchString(host) {
		return nil, errors.New("--host must be an SSH alias or user@host; configure ports and jump hosts in ~/.ssh/config")
	}
	if account == "" {
		return nil, errors.New("--host requires an explicit --account on the remote host")
	}
	return []string{"-T", "-o", "ClearAllForwardings=yes", host, graphicalRemoteCommand + shellSingleQuote(account)}, nil
}

// graphicalRemoteCommand starts the remote helper. A non-interactive SSH
// command does not read the login profile, so the usual install directories
// are appended to PATH; a corr already on PATH still wins. The account is
// passed as $1 and never interpolated into the script.
const graphicalRemoteCommand = `exec sh -c 'PATH="$PATH:$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:/home/linuxbrew/.linuxbrew/bin"; exec corr auth login --web-stdio --account "$1"' corr-remote-login `

func runSSHGraphicalLogin(app *runtime, host, account string, port int) error {
	args, err := graphicalSSHArguments(host, account)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(app.context, graphicalLoginLifetime)
	defer cancel()
	command := exec.CommandContext(ctx, "ssh", args...) // #nosec G204 -- host is validated and remote account shell-quoted; no local shell.
	command.Stderr = app.stderr
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return errors.New("cannot start SSH; check installation and SSH host configuration")
	}
	defer func() { _ = input.Close(); cancel(); _ = command.Wait() }()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 3<<20)
	exchange := func(callCtx context.Context, action *application.GraphicalLoginAction) (loginui.Result, error) {
		// An interrupted exchange closes this SSH invocation; input is never retried.
		stop := context.AfterFunc(callCtx, cancel)
		defer stop()
		closed := fmt.Errorf("the SSH connection to %s closed before the login answered; any message above is from that host. Check that it is reachable and that the same Corresync version is installed there", host)
		if err := json.NewEncoder(input).Encode(graphicalWireRequest{Version: graphicalWireVersion, Action: action}); err != nil {
			return loginui.Result{}, closed
		}
		if !scanner.Scan() {
			return loginui.Result{}, closed
		}
		var response graphicalWireResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || response.Version != graphicalWireVersion {
			return loginui.Result{}, fmt.Errorf("%s answered with a different login protocol; install the same Corresync version on both computers", host)
		}
		if response.Failed {
			return loginui.Result{}, fmt.Errorf("the login on %s failed; any message above is from that host", host)
		}
		return response.Result, nil
	}
	return serveGraphicalUI(ctx, app, exchange, port, host, graphicalLoginLifetime)
}
