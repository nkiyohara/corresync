// Package credential resolves secrets from facilities owned by the signed-in
// human. It never stores them and never participates in discovery.
package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/nkiyohara/corresync/internal/config"
)

const (
	keyringService     = "corresync"
	maximumSecretBytes = 64 << 10
)

// Secret is an in-memory value whose owned byte slice is overwritten on Close.
type Secret struct {
	value []byte
}

// String returns the value required by protocol clients. Callers must keep the
// resulting string local to one authenticated adapter construction.
func (secret *Secret) String() string {
	if secret == nil {
		return ""
	}
	return string(secret.value)
}

// CopyBytes returns a mutable caller-owned copy. Callers must overwrite the
// returned slice after the protocol client has consumed it.
func (secret *Secret) CopyBytes() []byte {
	if secret == nil {
		return nil
	}
	return bytes.Clone(secret.value)
}

// Close overwrites the owned mutable storage.
func (secret *Secret) Close() error {
	if secret == nil {
		return nil
	}
	for index := range secret.value {
		secret.value[index] = 0
	}
	secret.value = nil
	return nil
}

type keyringGetter func(service, key string) (string, error)
type helperRunner func(context.Context, []string, []byte) ([]byte, error)

// Options supports deterministic adapters and synthetic tests.
type Options struct {
	Helper  []string
	Keyring keyringGetter
	Run     helperRunner
}

// Resolver implements the two accepted external credential backends.
type Resolver struct {
	helper  []string
	keyring keyringGetter
	run     helperRunner
}

// New constructs a resolver without reading a credential.
func New(options Options) (*Resolver, error) {
	if len(options.Helper) > 16 {
		return nil, errors.New("credential helper has too many arguments")
	}
	for _, argument := range options.Helper {
		if argument == "" || len(argument) > 4096 ||
			strings.ContainsAny(argument, "\r\n\x00") {
			return nil, errors.New("credential helper argument is malformed")
		}
	}
	getter := options.Keyring
	if getter == nil {
		getter = keyring.Get
	}
	runner := options.Run
	if runner == nil {
		runner = runHelper
	}
	return &Resolver{
		helper:  append([]string(nil), options.Helper...),
		keyring: getter,
		run:     runner,
	}, nil
}

// Resolve reads exactly one consented reference. Errors name only the backend
// and never include the secret or helper output.
func (resolver *Resolver) Resolve(
	ctx context.Context,
	reference config.CredentialRef,
) (*Secret, error) {
	if !reference.Consent {
		return nil, errors.New("credential use has not been explicitly consented")
	}
	if reference.Key == "" || len(reference.Key) > 256 ||
		strings.ContainsAny(reference.Key, "\r\n\x00") {
		return nil, errors.New("credential key is malformed")
	}
	var value string
	switch reference.Backend {
	case config.CredentialOSKeyring:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resolved, err := resolver.keyring(keyringService, reference.Key)
		if err != nil {
			return nil, fmt.Errorf("read OS credential %q: %w", reference.Key, err)
		}
		value = resolved
	case config.CredentialHelper:
		if len(resolver.helper) == 0 {
			return nil, errors.New("credential helper is not configured")
		}
		request, err := json.Marshal(struct {
			Version   int    `json:"version"`
			Operation string `json:"operation"`
			Key       string `json:"key"`
		}{Version: 1, Operation: "get", Key: reference.Key})
		if err != nil {
			return nil, err
		}
		output, err := resolver.run(ctx, resolver.helper, append(request, '\n'))
		if err != nil {
			return nil, fmt.Errorf("credential helper failed: %w", err)
		}
		if len(output) > maximumSecretBytes {
			return nil, errors.New("credential helper output exceeds the limit")
		}
		value = strings.TrimSuffix(string(output), "\n")
		value = strings.TrimSuffix(value, "\r")
	default:
		return nil, errors.New("unsupported credential backend")
	}
	if value == "" || len(value) > maximumSecretBytes ||
		strings.ContainsAny(value, "\r\n\x00") {
		return nil, errors.New("external credential is empty or malformed")
	}
	return &Secret{value: []byte(value)}, nil
}

func runHelper(ctx context.Context, arguments []string, input []byte) ([]byte, error) {
	command := exec.CommandContext(ctx, arguments[0], arguments[1:]...) // #nosec G204 -- the executable is an explicit local configuration choice.
	command.Stdin = bytes.NewReader(input)
	command.Env = helperEnvironment(os.Environ())
	var output bytes.Buffer
	command.Stdout = &boundedWriter{writer: &output, remaining: maximumSecretBytes + 1}
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, err
	}
	if output.Len() > maximumSecretBytes {
		return nil, errors.New("output exceeds the limit")
	}
	return output.Bytes(), nil
}

type boundedWriter struct {
	writer    io.Writer
	remaining int
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	if len(value) > writer.remaining {
		allowed := writer.remaining
		if allowed > 0 {
			_, _ = writer.writer.Write(value[:allowed])
			writer.remaining = 0
		}
		return len(value), nil
	}
	written, err := writer.writer.Write(value)
	writer.remaining -= written
	return written, err
}

func helperEnvironment(environment []string) []string {
	allowed := map[string]struct{}{
		"DBUS_SESSION_BUS_ADDRESS": {}, "HOME": {}, "LANG": {}, "LC_ALL": {},
		"LOCALAPPDATA": {}, "PATH": {}, "USERPROFILE": {}, "XDG_RUNTIME_DIR": {},
	}
	result := make([]string, 0, len(allowed))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, keep := allowed[name]; keep {
				result = append(result, entry)
			}
		}
	}
	return result
}
