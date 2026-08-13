package integrationlifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"go.yaml.in/yaml/v3"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/filelock"
)

func resolveYAMLAdapter(environment Environment, request Request) (string, bool, error) {
	if request.Host != agenthost.IDGoose || request.Scope != agenthost.ScopeUser {
		return "", false, nil
	}
	if !filepath.IsAbs(environment.ConfigDirectory) {
		return "", false, errors.New("integration config directory must be absolute")
	}
	parts := []string{environment.ConfigDirectory, "goose", "config.yaml"}
	if environment.GOOS == "windows" {
		parts = []string{environment.ConfigDirectory, "Block", "goose", "config", "config.yaml"}
	}
	return filepath.Clean(filepath.Join(parts...)), true, nil
}

type yamlSnapshot struct {
	root        *yaml.Node
	data        []byte
	fingerprint string
	exists      bool
	mode        os.FileMode
}

type YAMLStore struct{}

func (YAMLStore) Inspect(path string, request Request, environment Environment) (Inspection, error) {
	if err := validateTargetParents(path, request, environment); err != nil {
		return inspectionForFileError(path, request.Scope, err), nil
	}
	snapshot, err := loadYAMLSnapshot(path)
	if err != nil {
		return inspectionForFileError(path, request.Scope, err), nil
	}
	inspection := Inspection{Scope: request.Scope, Path: path, Fingerprint: snapshot.fingerprint}
	if !snapshot.exists {
		inspection.State = StateAbsent
		inspection.Detail = "The named Corresync extension is not present in the documented goose configuration."
		return inspection, nil
	}
	extensions, err := yamlMappingPath(snapshot.root, false, "extensions")
	if err != nil {
		inspection.State = StateMalformed
		inspection.Detail = "The goose configuration has an incompatible extensions object shape."
		return inspection, nil //nolint:nilerr // Shape failures are represented by the typed inspection state.
	}
	entry := yamlMapValue(extensions, request.ServerName)
	if entry == nil {
		inspection.State = StateAbsent
		inspection.Detail = "The named Corresync extension is not registered."
		return inspection, nil
	}
	inspection.State = classifyGooseEntry(entry, request)
	switch inspection.State {
	case StateHealthy:
		inspection.Detail = "goose has the expected absolute Corresync stdio extension contract."
	case StateDisabled:
		inspection.Detail = "The Corresync goose extension is present but disabled."
	case StateStalePath:
		inspection.Detail = "The Corresync goose extension has stale Corresync-owned launch fields."
	case StateNameConflict, StateAbsent, StateVersionDrift, StateMalformed, StateUnreadable, StateUnavailable:
		inspection.Detail = "The requested goose extension name belongs to a different command."
	}
	return inspection, nil
}

func loadYAMLSnapshot(path string) (yamlSnapshot, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return yamlSnapshot{}, errors.New("host configuration path must be clean and absolute")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		root := newYAMLDocument()
		return yamlSnapshot{root: root, fingerprint: absentFileFingerprint(path), mode: 0o600}, nil
	}
	if err != nil {
		return yamlSnapshot{}, err
	}
	if !info.Mode().IsRegular() || WritableByOtherUsers(info) || !ownedByCurrentUser(info) {
		return yamlSnapshot{}, errors.New("goose configuration has an unsafe type, owner, or mode")
	}
	if info.Size() > maximumHostConfigBytes {
		return yamlSnapshot{}, fmt.Errorf("goose configuration exceeds %d bytes", maximumHostConfigBytes)
	}
	// #nosec G304 -- path comes from the reviewed goose adapter.
	file, err := os.Open(path)
	if err != nil {
		return yamlSnapshot{}, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return yamlSnapshot{}, errors.New("goose configuration changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumHostConfigBytes+1))
	if err != nil {
		return yamlSnapshot{}, fmt.Errorf("read bounded goose configuration: %w", err)
	}
	if len(data) > maximumHostConfigBytes {
		return yamlSnapshot{}, fmt.Errorf("goose configuration exceeds %d bytes", maximumHostConfigBytes)
	}
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&root); err != nil {
		return yamlSnapshot{}, &malformedJSONError{err: fmt.Errorf("parse goose YAML: %w", err)}
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return yamlSnapshot{}, &malformedJSONError{err: errors.New("goose configuration must contain one YAML document")}
	}
	if err := validateYAMLTree(&root); err != nil {
		return yamlSnapshot{}, &malformedJSONError{err: err}
	}
	return yamlSnapshot{root: &root, data: data, fingerprint: fileFingerprint(data), exists: true, mode: info.Mode().Perm()}, nil
}

func newYAMLDocument() *yaml.Node {
	return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
}

func validateYAMLTree(root *yaml.Node) error {
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return errors.New("goose configuration root must be one mapping")
	}
	count := 0
	var walk func(*yaml.Node) error
	walk = func(node *yaml.Node) error {
		count++
		if count > 8192 {
			return errors.New("goose configuration exceeds the YAML node bound")
		}
		if node.Kind == yaml.AliasNode {
			return errors.New("goose configuration aliases are not supported for safe mutation")
		}
		for _, child := range node.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(root)
}

func yamlMappingPath(root *yaml.Node, create bool, path ...string) (*yaml.Node, error) {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, errors.New("invalid YAML document")
	}
	cursor := root.Content[0]
	for _, name := range path {
		if cursor.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("YAML field %q is not a mapping", name)
		}
		next := yamlMapValue(cursor, name)
		if next == nil {
			if !create {
				return nil, nil
			}
			next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			yamlMapSet(cursor, name, next)
		}
		if next.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("YAML field %q is not a mapping", name)
		}
		cursor = next
	}
	return cursor, nil
}

func yamlMapValue(mapping *yaml.Node, name string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == name {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func yamlMapSet(mapping *yaml.Node, name string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == name {
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, value,
	)
}

func yamlMapDelete(mapping *yaml.Node, name string) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == name {
			mapping.Content = append(mapping.Content[:index], mapping.Content[index+2:]...)
			return
		}
	}
}

func classifyGooseEntry(entry *yaml.Node, request Request) State {
	if entry.Kind != yaml.MappingNode {
		return StateNameConflict
	}
	command := yamlMapValue(entry, "cmd")
	if command == nil || command.Kind != yaml.ScalarNode {
		return StateNameConflict
	}
	staleCommand := command.Value != request.Executable
	if staleCommand && !ownedExecutable(command.Value) {
		return StateNameConflict
	}
	if enabled := yamlMapValue(entry, "enabled"); enabled != nil && enabled.Value == "false" {
		return StateDisabled
	}
	if staleCommand {
		return StateStalePath
	}
	for _, name := range hostAutoApprovalFields {
		if yamlMapValue(entry, name) != nil {
			return StateStalePath
		}
	}
	if kind := yamlMapValue(entry, "type"); kind == nil || kind.Value != "stdio" {
		return StateStalePath
	}
	if !yamlScalarEquals(yamlMapValue(entry, "name"), "!!str", request.ServerName) ||
		!yamlScalarEquals(yamlMapValue(entry, "display_name"), "!!str", "Corresync") ||
		!yamlScalarEquals(yamlMapValue(entry, "enabled"), "!!bool", "true") ||
		!yamlScalarEquals(yamlMapValue(entry, "bundled"), "!!bool", "false") ||
		!yamlScalarEquals(yamlMapValue(entry, "timeout"), "!!int", "300") {
		return StateStalePath
	}
	arguments := yamlMapValue(entry, "args")
	if !yamlStringSequenceEqual(arguments, request.Arguments) {
		return StateStalePath
	}
	environmentKeys := yamlMapValue(entry, "env_keys")
	environment := yamlMapValue(entry, "envs")
	if environmentKeys == nil || environmentKeys.Kind != yaml.SequenceNode || len(environmentKeys.Content) != 0 ||
		environment == nil || environment.Kind != yaml.MappingNode || len(environment.Content) != 0 {
		return StateStalePath
	}
	return StateHealthy
}

func yamlScalarEquals(node *yaml.Node, tag, value string) bool {
	return node != nil && node.Kind == yaml.ScalarNode && node.Tag == tag && node.Value == value
}

func yamlStringSequenceEqual(sequence *yaml.Node, expected []string) bool {
	if sequence == nil || sequence.Kind != yaml.SequenceNode || len(sequence.Content) != len(expected) {
		return false
	}
	for index := range expected {
		if sequence.Content[index].Kind != yaml.ScalarNode || sequence.Content[index].Value != expected[index] {
			return false
		}
	}
	return true
}

func (YAMLStore) Apply(ctx context.Context, path string, request Request, environment Environment, expectedFingerprint string, remove bool) (returnErr error) {
	if err := prepareTargetParent(path, request, environment); err != nil {
		return err
	}
	lock, err := filelock.AcquireSidecar(ctx, path+".corresync.lock")
	if err != nil {
		return fmt.Errorf("acquire goose configuration lock: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	if err := validateTargetParents(path, request, environment); err != nil {
		return err
	}
	snapshot, err := loadYAMLSnapshot(path)
	if err != nil {
		return err
	}
	if snapshot.fingerprint != expectedFingerprint {
		return errors.New("goose configuration changed after preview")
	}
	extensions, err := yamlMappingPath(snapshot.root, !remove, "extensions")
	if err != nil {
		return err
	}
	if remove {
		if extensions != nil {
			yamlMapDelete(extensions, request.ServerName)
		}
	} else {
		entry := yamlMapValue(extensions, request.ServerName)
		if entry == nil {
			entry = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			yamlMapSet(extensions, request.ServerName, entry)
		}
		setGooseEntry(entry, request)
	}
	var encoded bytes.Buffer
	encoder := yaml.NewEncoder(&encoded)
	encoder.SetIndent(2)
	if err := encoder.Encode(snapshot.root); err != nil {
		return fmt.Errorf("encode goose configuration: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close goose configuration encoder: %w", err)
	}
	if encoded.Len() > maximumHostConfigBytes {
		return fmt.Errorf("updated goose configuration exceeds %d bytes", maximumHostConfigBytes)
	}
	if err := validateTargetParents(path, request, environment); err != nil {
		return err
	}
	if snapshot.exists {
		if err := writeAtomicPrivate(path+".corresync.bak", snapshot.data, snapshot.mode); err != nil {
			return fmt.Errorf("write goose recovery copy: %w", err)
		}
	}
	mode := snapshot.mode
	if !snapshot.exists {
		mode = 0o600
	}
	return writeAtomicPrivate(path, encoded.Bytes(), mode)
}

func setGooseEntry(entry *yaml.Node, request Request) {
	entry.Kind = yaml.MappingNode
	entry.Tag = "!!map"
	for _, name := range hostAutoApprovalFields {
		yamlMapDelete(entry, name)
	}
	yamlMapSet(entry, "type", yamlScalar("stdio"))
	yamlMapSet(entry, "name", yamlScalar(request.ServerName))
	yamlMapSet(entry, "display_name", yamlScalar("Corresync"))
	yamlMapSet(entry, "enabled", yamlTypedScalar("!!bool", "true"))
	yamlMapSet(entry, "bundled", yamlTypedScalar("!!bool", "false"))
	yamlMapSet(entry, "cmd", yamlScalar(request.Executable))
	arguments := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, argument := range request.Arguments {
		arguments.Content = append(arguments.Content, yamlScalar(argument))
	}
	yamlMapSet(entry, "args", arguments)
	yamlMapSet(entry, "env_keys", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	yamlMapSet(entry, "envs", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	yamlMapSet(entry, "timeout", yamlTypedScalar("!!int", strconv.Itoa(300)))
}

func yamlScalar(value string) *yaml.Node {
	return yamlTypedScalar("!!str", value)
}

func yamlTypedScalar(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}
