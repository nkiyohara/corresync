package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
)

type terminalLoginClient interface {
	TerminalLogin(context.Context, daemonapi.TerminalLoginInput, domain.Caller) (daemonapi.TerminalLoginResult, error)
}

func runTerminalLogin(
	app *runtime,
	client terminalLoginClient,
	account domain.AccountID,
) (returnErr error) {
	input, err := interactiveTerminalInput(app)
	if err != nil {
		return err
	}
	state, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return fmt.Errorf("enable terminal key relay: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, term.Restore(int(input.Fd()), state)) }()
	ctx, cancel := context.WithCancel(app.context)
	defer cancel()
	cancellable, err := cancelreader.NewReader(input)
	if err != nil {
		return err
	}
	reader, closeReader := startTerminalInput(cancellable, cancel)
	defer func() { returnErr = errors.Join(returnErr, closeReader()) }()
	terminalApp := &runtime{
		context: ctx, stdin: input, stdout: terminalOutput{app.stdout}, processID: app.processID,
	}
	return runTerminalLoginLoop(terminalApp, client, account, reader)
}

func runTerminalLoginLoop(app *runtime, client terminalLoginClient, account domain.AccountID, reader *terminalInput) error {
	result, err := client.TerminalLogin(app.context, daemonapi.TerminalLoginInput{
		Account: account,
	}, app.caller())
	if err != nil {
		return err
	}
	sessionID := result.SessionID
	defer func() {
		if sessionID == "" {
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = client.TerminalLogin(cleanupContext, daemonapi.TerminalLoginInput{
			Account: account, SessionID: sessionID,
			Action: &daemonapi.TerminalLoginAction{Type: "cancel"},
		}, app.caller())
	}()

	for result.Status == "pending" {
		if result.View == nil {
			return errors.New("terminal login returned no browser view")
		}
		if err := writeTerminalLoginView(app, *result.View); err != nil {
			return err
		}
		selection, err := readTerminalSelection(app, reader)
		if err != nil {
			return err
		}
		switch selection {
		case "q", "quit":
			result, err = client.TerminalLogin(app.context, daemonapi.TerminalLoginInput{
				Account: account, SessionID: result.SessionID,
				Action: &daemonapi.TerminalLoginAction{Type: "cancel"},
			}, app.caller())
			if err != nil {
				return err
			}
			sessionID = ""
			_, err = fmt.Fprintln(app.stdout, "Terminal login cancelled.")
			return err
		case "r", "refresh":
			result, err = advanceTerminalLogin(app, client, result, daemonapi.TerminalLoginAction{Type: "refresh"})
			if err != nil {
				return err
			}
			continue
		}

		position, err := strconv.Atoi(selection)
		if err != nil || position < 1 || position > len(result.View.Controls) {
			if _, writeErr := fmt.Fprintln(app.stdout, "Choose a listed control, r to refresh, or q to cancel."); writeErr != nil {
				return writeErr
			}
			continue
		}
		control := result.View.Controls[position-1]
		if control.Disabled {
			if _, err := fmt.Fprintln(app.stdout, "That control is disabled."); err != nil {
				return err
			}
			continue
		}
		if control.Kind == "activate" {
			previous := result.View
			result, err = advanceTerminalLogin(app, client, result, daemonapi.TerminalLoginAction{
				Type: "activate", ControlID: control.ID,
			})
			if err != nil {
				return err
			}
			if terminalLoginViewUnchanged(previous, result) {
				if err := writeTerminalProgressHint(app); err != nil {
					return err
				}
			}
			continue
		}

		result, err = advanceTerminalLogin(app, client, result, daemonapi.TerminalLoginAction{
			Type: "focus", ControlID: control.ID,
		})
		if err != nil {
			return err
		}
		if result.Status == "pending" {
			if err := relayTerminalKeys(app, client, control, reader, &result); err != nil {
				return err
			}
		}
	}
	if result.Status != "authenticated" {
		return fmt.Errorf("terminal login ended in unexpected state %q", result.Status)
	}
	sessionID = ""
	_, err = fmt.Fprintf(app.stdout, "Authenticated Outlook Web account %q.\n", account)
	return err
}

func interactiveTerminalInput(app *runtime) (*os.File, error) {
	source := app.stdin
	for {
		accessible, ok := source.(*settingsAccessibleReader)
		if !ok {
			break
		}
		source = accessible.source
	}
	input, ok := source.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) {
		return nil, errors.New("terminal login requires an interactive TTY; piped input is not accepted")
	}
	return input, nil
}

func writeTerminalLoginView(app *runtime, view daemonapi.TerminalLoginView) error {
	if _, err := fmt.Fprintln(app.stdout); err != nil {
		return err
	}
	if view.Title != "" {
		if _, err := fmt.Fprintln(app.stdout, view.Title); err != nil {
			return err
		}
	}
	if view.Origin != "" {
		if _, err := fmt.Fprintf(app.stdout, "Origin: %s\n", view.Origin); err != nil {
			return err
		}
	}
	if view.Text != "" {
		if _, err := fmt.Fprintln(app.stdout, view.Text); err != nil {
			return err
		}
	}
	for index, control := range view.Controls {
		qualifier := control.Kind
		if control.Sensitive {
			qualifier += ", hidden input"
		}
		if control.Disabled {
			qualifier += ", disabled"
		}
		if _, err := fmt.Fprintf(app.stdout, "[%d] %s (%s)\n", index+1, control.Name, qualifier); err != nil {
			return err
		}
	}
	return nil
}

func readTerminalSelection(app *runtime, reader *terminalInput) (string, error) {
	if _, err := fmt.Fprint(app.stdout, "> "); err != nil {
		return "", err
	}
	var selection []rune
	for {
		character, err := reader.readKey(app.context)
		if err != nil {
			return "", fmt.Errorf("read terminal login selection: %w", err)
		}
		switch character {
		case 3, 4:
			return "", context.Canceled
		case '\r', '\n':
			_, err := fmt.Fprintln(app.stdout)
			return strings.ToLower(string(selection)), err
		case '\b', 127:
			if len(selection) > 0 {
				selection = selection[:len(selection)-1]
				if _, err := fmt.Fprint(app.stdout, "\b \b"); err != nil {
					return "", err
				}
			}
		default:
			// The menu accepts only digits or its named commands. Do not echo arbitrary
			// input left over from a field or paste when the page changes.
			if len(selection) < 16 && strings.ContainsRune("0123456789rRefFshHqQuiItT", character) {
				selection = append(selection, character)
				if _, err := fmt.Fprint(app.stdout, string(character)); err != nil {
					return "", err
				}
			}
		}
	}
}

func relayTerminalKeys(
	app *runtime,
	client terminalLoginClient,
	control daemonapi.TerminalLoginControl,
	reader *terminalInput,
	result *daemonapi.TerminalLoginResult,
) (returnErr error) {
	if _, err := fmt.Fprintln(app.stdout, "Type into the browser field; Enter submits, Esc returns to the control list. Use Backspace to correct input."); err != nil {
		return err
	}
	visibleCharacters := 0
	for result.Status == "pending" {
		character, err := reader.readKey(app.context)
		if err != nil {
			return fmt.Errorf("read terminal browser key: %w", err)
		}
		action := daemonapi.TerminalLoginAction{Type: "key", ControlID: control.ID}
		switch character {
		case 3:
			return context.Canceled
		case 27:
			_, err = fmt.Fprintln(app.stdout)
			return err
		case '\r', '\n':
			action.Key = "enter"
		case '\b', 127:
			action.Key = "backspace"
			if visibleCharacters > 0 {
				visibleCharacters--
				if _, err := fmt.Fprint(app.stdout, "\b \b"); err != nil {
					return err
				}
			}
		case '\t':
			action.Key = "tab"
		default:
			if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
				continue
			}
			action.Key = string(character)
			visibleCharacters++
			display := action.Key
			if control.Sensitive {
				display = "*"
			}
			if _, err := fmt.Fprint(app.stdout, display); err != nil {
				return err
			}
		}
		previous := result.View
		*result, err = advanceTerminalLogin(app, client, *result, action)
		if err != nil {
			return err
		}
		if action.Key == "enter" && terminalLoginViewUnchanged(previous, *result) {
			if err := writeTerminalProgressHint(app); err != nil {
				return err
			}
		}
		if action.Key == "enter" || action.Key == "tab" {
			_, err = fmt.Fprintln(app.stdout)
			return err
		}
	}
	return nil
}

func terminalLoginViewUnchanged(
	previous *daemonapi.TerminalLoginView,
	current daemonapi.TerminalLoginResult,
) bool {
	return current.Status == "pending" && previous != nil && current.View != nil &&
		terminalLoginViewsEqual(*previous, *current.View)
}

func writeTerminalProgressHint(app *runtime) error {
	_, err := fmt.Fprintln(
		app.stdout,
		"No visible page change yet. Wait a moment and press r to refresh, or choose another control.",
	)
	return err
}

func advanceTerminalLogin(
	app *runtime,
	client terminalLoginClient,
	current daemonapi.TerminalLoginResult,
	action daemonapi.TerminalLoginAction,
) (daemonapi.TerminalLoginResult, error) {
	return client.TerminalLogin(app.context, daemonapi.TerminalLoginInput{
		Account: current.Account, SessionID: current.SessionID, Action: &action,
	}, app.caller())
}
