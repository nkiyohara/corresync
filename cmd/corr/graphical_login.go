package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	goruntime "runtime"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/loginui"
)

const graphicalWireVersion = 1
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
			return loginui.Result{}, errors.New("remote browser login failed")
		}
		sessionID = result.SessionID
		return loginui.Result{Status: result.Status, Frame: result.Frame}, nil
	}
}

func runGraphicalLogin(app *runtime, client terminalLoginClient, account domain.AccountID, port int, stdio bool) error {
	ctx, cancel := context.WithTimeout(app.context, graphicalLoginLifetime)
	defer cancel()
	exchange := graphicalExchange(app, client, account)
	if stdio {
		return serveGraphicalStdio(ctx, app.stdin, app.stdout, exchange)
	}
	return serveGraphicalUI(ctx, app, exchange, port, true)
}

func serveGraphicalUI(ctx context.Context, app *runtime, exchange loginui.Exchange, port int, remote bool) error {
	server, err := loginui.New(ctx, port, exchange)
	if err != nil {
		return err
	}
	// Print the capability only to the explicitly invoked human CLI, never logs.
	if remote {
		_, err = fmt.Fprintf(app.stdout, "On your Mac/local computer, run (replace SSH_HOST with its reachable SSH alias):\n  ssh -N -L 127.0.0.1:%d:127.0.0.1:%d SSH_HOST\nThen open on your Mac/local computer:\n  %s\nThe account session stays on this remote host. Keep this link private. Expires in 10 minutes; Ctrl-C cancels.\n", server.Port(), server.Port(), server.URL())
	} else {
		_, err = fmt.Fprintf(app.stdout, "Open locally: %s\nThe account session stays on the SSH host. Keep this link private. Expires in 10 minutes; Ctrl-C cancels.\n", server.URL())
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
	err = server.Run(ctx)
	if err == nil {
		_, err = fmt.Fprintln(app.stdout, "Login view closed. If sign-in completed, the authenticated account remains on the remote host.")
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
		if err := decoder.Decode(&request); err != nil || request.Version != graphicalWireVersion {
			return errors.New("incompatible graphical login protocol; update Corresync on both computers")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return errors.New("invalid graphical login request")
		}
		if request.Action != nil {
			if err := request.Action.Validate(); err != nil {
				return err
			}
		}
		result, err := exchange(ctx, request.Action)
		response := graphicalWireResponse{Version: graphicalWireVersion, Result: result, Failed: err != nil}
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			return errors.New("login connection closed")
		}
		if err != nil || result.Status == "authenticated" || result.Status == "cancelled" {
			return err
		}
	}
}

var graphicalSSHHostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]*$`)

func graphicalSSHArguments(host, account string) ([]string, error) {
	if !graphicalSSHHostPattern.MatchString(host) {
		return nil, errors.New("--host must be an SSH alias or user@host; configure ports and jump hosts in ~/.ssh/config")
	}
	if account == "" {
		return nil, errors.New("--host requires an explicit --account on the remote host")
	}
	return []string{"-T", "-o", "ClearAllForwardings=yes", host, "corr auth login --web-stdio --account " + shellSingleQuote(account)}, nil
}

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
		if err := json.NewEncoder(input).Encode(graphicalWireRequest{Version: graphicalWireVersion, Action: action}); err != nil {
			return loginui.Result{}, errors.New("SSH login connection closed")
		}
		if !scanner.Scan() {
			return loginui.Result{}, errors.New("SSH login connection closed; check host and matching Corresync versions")
		}
		var response graphicalWireResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || response.Version != graphicalWireVersion || response.Failed {
			return loginui.Result{}, errors.New("SSH login failed; update Corresync on both computers and check the remote account")
		}
		return response.Result, nil
	}
	return serveGraphicalUI(ctx, app, exchange, port, false)
}
