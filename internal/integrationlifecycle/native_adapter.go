package integrationlifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

type nativeAdapter struct {
	host         agenthost.ID
	kind         Component
	inspect      Command
	sourceCheck  *Command
	install      func(Request, packageDescriptor) Command
	remove       func(Request, packageDescriptor) Command
	removeSource func(Request, packageDescriptor) Command
	addSource    func(Request, packageDescriptor) Command
	selector     string
}

func resolveNativeAdapter(environment Environment, request Request) (nativeAdapter, packageDescriptor, bool, error) {
	if !nativePackageScope(request.Host, request.Scope) {
		return nativeAdapter{}, packageDescriptor{}, false, nil
	}
	descriptor, ok, err := (PackageStore{}).Describe(environment, request.Host)
	if !ok {
		return nativeAdapter{}, packageDescriptor{}, false, err
	}
	directory := ""
	if request.Scope != agenthost.ScopeUser {
		directory = request.ProjectDirectory
	}
	command := func(executable string, arguments ...string) Command {
		return Command{Executable: executable, Arguments: arguments, WorkingDirectory: directory}
	}
	adapter := nativeAdapter{host: request.Host, kind: descriptor.kind}
	if err != nil {
		return adapter, descriptor, true, err
	}
	//nolint:exhaustive // PackageStore.Describe admits only the four native-package hosts below.
	switch request.Host {
	case agenthost.IDCodex:
		if request.Scope != agenthost.ScopeUser {
			return nativeAdapter{}, packageDescriptor{}, false, nil
		}
		adapter.inspect = command("codex", "plugin", "list", "--json")
		source := command("codex", "plugin", "marketplace", "list", "--json")
		adapter.sourceCheck = &source
		adapter.selector = "corresync@" + managedMarketplaceName
		adapter.addSource = func(_ Request, descriptor packageDescriptor) Command {
			return command("codex", "plugin", "marketplace", "add", "--json", descriptor.installSource)
		}
		adapter.install = func(_ Request, _ packageDescriptor) Command {
			return command("codex", "plugin", "add", "--json", adapter.selector)
		}
		adapter.remove = func(_ Request, _ packageDescriptor) Command {
			return command("codex", "plugin", "remove", "--json", adapter.selector)
		}
		adapter.removeSource = func(_ Request, _ packageDescriptor) Command {
			return command("codex", "plugin", "marketplace", "remove", "--json", managedMarketplaceName)
		}
	case agenthost.IDClaudeCode:
		adapter.inspect = command("claude", "plugin", "list", "--json")
		source := command("claude", "plugin", "marketplace", "list", "--json")
		adapter.sourceCheck = &source
		adapter.selector = "corresync@" + managedMarketplaceName
		adapter.addSource = func(request Request, descriptor packageDescriptor) Command {
			return command("claude", "plugin", "marketplace", "add", "--scope", string(request.Scope), descriptor.installSource)
		}
		adapter.install = func(request Request, _ packageDescriptor) Command {
			return command("claude", "plugin", "install", "--scope", string(request.Scope), adapter.selector)
		}
		adapter.remove = func(request Request, _ packageDescriptor) Command {
			return command("claude", "plugin", "uninstall", "--scope", string(request.Scope), adapter.selector)
		}
		adapter.removeSource = func(request Request, _ packageDescriptor) Command {
			return command("claude", "plugin", "marketplace", "remove", "--scope", string(request.Scope), managedMarketplaceName)
		}
	case agenthost.IDGitHubCopilot:
		if request.Scope != agenthost.ScopeUser {
			return nativeAdapter{}, packageDescriptor{}, false, nil
		}
		adapter.inspect = command("copilot", "plugins", "list", "--kind", "plugin", "--scope", "user", "--json")
		adapter.selector = "corresync"
		adapter.install = func(_ Request, descriptor packageDescriptor) Command {
			return command("copilot", "plugin", "install", descriptor.installSource)
		}
		adapter.remove = func(_ Request, _ packageDescriptor) Command {
			return command("copilot", "plugin", "uninstall", adapter.selector)
		}
	case agenthost.IDGeminiCLI:
		if request.Scope != agenthost.ScopeUser {
			return nativeAdapter{}, packageDescriptor{}, false, nil
		}
		adapter.inspect = command("gemini", "extensions", "list", "--output-format", "json")
		adapter.selector = "corresync"
		adapter.install = func(_ Request, descriptor packageDescriptor) Command {
			return command("gemini", "extensions", "install", descriptor.installSource, "--consent", "--skip-settings")
		}
		adapter.remove = func(_ Request, _ packageDescriptor) Command {
			return command("gemini", "extensions", "uninstall", adapter.selector)
		}
	default:
		return nativeAdapter{}, packageDescriptor{}, false, nil
	}
	return adapter, descriptor, true, nil
}

func nativePackageScope(host agenthost.ID, scope agenthost.Scope) bool {
	switch host {
	case agenthost.IDCodex, agenthost.IDGitHubCopilot, agenthost.IDGeminiCLI:
		return scope == agenthost.ScopeUser
	case agenthost.IDClaudeCode:
		return scope == agenthost.ScopeLocal || scope == agenthost.ScopeProject || scope == agenthost.ScopeUser
	case agenthost.IDClaudeDesktop, agenthost.IDKiro, agenthost.IDQwenCode, agenthost.IDQoder,
		agenthost.IDKimiCode, agenthost.IDVSCode,
		agenthost.IDCursor, agenthost.IDWindsurf, agenthost.IDOpenCode, agenthost.IDCline,
		agenthost.IDRooCode, agenthost.IDZed, agenthost.IDGoose:
		return false
	}
	return false
}

type nativeInspection struct {
	components  []ComponentInspection
	fingerprint string
}

func (engine Engine) inspectNative(ctx context.Context, request Request, adapter nativeAdapter, descriptor packageDescriptor) (nativeInspection, error) {
	if engine.Executor == nil {
		component := ComponentInspection{
			Component: adapter.kind, State: StateUnavailable, ExpectedVersion: descriptor.version,
			Detail: "The reviewed native package command cannot be executed in this runtime.",
		}
		return nativeInspection{components: []ComponentInspection{component}}, nil
	}
	stageState, stageVersion, stageFingerprint, stageErr := (PackageStore{}).InspectTarget(descriptor)
	if stageErr != nil && stageState != StateNameConflict {
		component := ComponentInspection{
			Component: adapter.kind, State: StateUnreadable, ExpectedVersion: descriptor.version,
			Detail: "The Corresync-managed native package source is unsafe or unreadable.",
		}
		return nativeInspection{components: []ComponentInspection{component}}, nil
	}
	packageExecution, err := engine.Executor.Run(ctx, adapter.inspect, maximumInspectionBytes)
	if err != nil {
		return nativeInspection{}, fmt.Errorf("inspect %s native package: %w", request.Host, err)
	}
	packageComponent := classifyNativePackage(adapter, descriptor, stageState, stageVersion, packageExecution)
	stageDetail := "The private Corresync package staging directory is current."
	switch stageState {
	case StateAbsent:
		stageDetail = "The private Corresync package staging directory is absent."
	case StateVersionDrift:
		stageDetail = "The private Corresync package staging directory is stale."
	case StateNameConflict:
		stageDetail = "The managed package target is not Corresync-owned."
	case StateHealthy, StateDisabled, StateStalePath, StateMalformed, StateUnreadable, StateUnavailable:
	}
	stageComponent := ComponentInspection{
		Component: ComponentStage, State: stageState, Version: stageVersion,
		ExpectedVersion: descriptor.version, Fingerprint: stageFingerprint, Detail: stageDetail,
	}
	components := []ComponentInspection{packageComponent, stageComponent}
	hash := sha256.New()
	_, _ = hash.Write([]byte(inspectionFingerprint(packageExecution, strings.TrimSpace(string(packageExecution.Output)))))
	_, _ = hash.Write([]byte(descriptor.sourceFingerprint))
	_, _ = hash.Write([]byte(string(stageState)))
	_, _ = hash.Write([]byte(stageVersion))
	if adapter.sourceCheck != nil {
		sourceExecution, runErr := engine.Executor.Run(ctx, *adapter.sourceCheck, maximumInspectionBytes)
		if runErr != nil {
			return nativeInspection{}, fmt.Errorf("inspect %s package source: %w", request.Host, runErr)
		}
		sourceComponent := classifyNativeSource(adapter, descriptor, sourceExecution)
		components = append(components, sourceComponent)
		_, _ = hash.Write([]byte(inspectionFingerprint(sourceExecution, strings.TrimSpace(string(sourceExecution.Output)))))
	}
	return nativeInspection{components: components, fingerprint: hex.EncodeToString(hash.Sum(nil))}, nil
}

func classifyNativePackage(adapter nativeAdapter, descriptor packageDescriptor, stageState State, stageVersion string, execution Execution) ComponentInspection {
	component := ComponentInspection{Component: adapter.kind, ExpectedVersion: descriptor.version}
	if execution.Truncated {
		component.State = StateMalformed
		component.Detail = "The host's native package inventory exceeded the bounded parser limit."
		return component
	}
	if !execution.Started {
		component.State = StateUnavailable
		component.Detail = "The host's native package command is unavailable."
		return component
	}
	if execution.ExitCode != 0 {
		component.State = StateUnreadable
		component.Detail = "The host could not inspect its native package registry."
		return component
	}
	output := strings.TrimSpace(string(execution.Output))
	if output == "" {
		component.State = StateAbsent
		component.Detail = "The Corresync native package is not installed."
		return component
	}
	entry, found, valid := findNativePackage(output, adapter)
	if !valid {
		component.State = StateMalformed
		component.Detail = "The host returned malformed native package inventory."
		return component
	}
	if !found {
		component.State = StateAbsent
		component.Detail = "The Corresync native package is not installed."
		return component
	}
	if stageState == StateAbsent || stageState == StateNameConflict {
		component.State = StateNameConflict
		component.Detail = "The host package name is not bound to a Corresync-managed package source."
		return component
	}
	component.Version = entry.version
	if !entry.enabled {
		component.State = StateDisabled
		component.Detail = "The Corresync native package is installed but disabled."
		return component
	}
	if entry.version != descriptor.version || stageState == StateVersionDrift || stageVersion != descriptor.version {
		component.State = StateVersionDrift
		component.Detail = "The Corresync native package version does not match this CLI release."
		return component
	}
	component.State = StateHealthy
	component.Detail = "The host reports the matching Corresync native package version."
	return component
}

type nativePackageEntry struct {
	version string
	enabled bool
}

func findNativePackage(output string, adapter nativeAdapter) (nativePackageEntry, bool, bool) {
	var value any
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		return nativePackageEntry{}, false, false
	}
	wantIDs := []string{adapter.selector}
	entries, valid := nativeInventoryEntries(value, adapter.host)
	if !valid {
		return nativePackageEntry{}, false, false
	}
	for _, entry := range entries {
		identity := firstString(entry, "pluginId", "id", "name")
		if !containsExact(wantIDs, identity) {
			continue
		}
		enabled := true
		if value, ok := entry["enabled"].(bool); ok {
			enabled = value
		}
		if value, ok := entry["isActive"].(bool); ok {
			enabled = value
		}
		return nativePackageEntry{version: firstString(entry, "version"), enabled: enabled}, true, true
	}
	return nativePackageEntry{}, false, true
}

func nativeInventoryEntries(value any, host agenthost.ID) ([]map[string]any, bool) {
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case map[string]any:
		keys := []string{"plugins", "resources", "items"}
		if host == agenthost.IDCodex {
			keys = []string{"installed"}
		}
		for _, key := range keys {
			if values, ok := typed[key].([]any); ok {
				raw = values
				break
			}
		}
		if raw == nil {
			return nil, false
		}
	default:
		return nil, false
	}
	entries := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		entries = append(entries, entry)
	}
	return entries, true
}

func firstString(document map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := document[name].(string); ok {
			return value
		}
	}
	return ""
}

func containsExact(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func classifyNativeSource(adapter nativeAdapter, descriptor packageDescriptor, execution Execution) ComponentInspection {
	component := ComponentInspection{Component: ComponentSource, ExpectedVersion: descriptor.version}
	if execution.Truncated {
		component.State = StateMalformed
		component.Detail = "The host's package-source inventory exceeded the bounded parser limit."
		return component
	}
	if !execution.Started {
		component.State = StateUnavailable
		component.Detail = "The host's package-source command is unavailable."
		return component
	}
	if execution.ExitCode != 0 {
		component.State = StateUnreadable
		component.Detail = "The host could not inspect its package sources."
		return component
	}
	output := strings.TrimSpace(string(execution.Output))
	if output == "" {
		component.State = StateAbsent
		component.Detail = "The private Corresync package source is not registered."
		return component
	}
	var value any
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		component.State = StateMalformed
		component.Detail = "The host returned malformed package-source inventory."
		return component
	}
	nameFound, targetFound, valid := findMarketplace(value, descriptor.targetRoot, adapter.host)
	if !valid {
		component.State = StateMalformed
		component.Detail = "The host returned malformed package-source inventory."
		return component
	}
	if !nameFound {
		component.State = StateAbsent
		component.Detail = "The private Corresync package source is not registered."
		return component
	}
	if !targetFound {
		component.State = StateNameConflict
		component.Detail = "The managed package-source name belongs to another location."
		return component
	}
	component.State = StateHealthy
	component.Detail = "The host knows the private Corresync package source."
	return component
}

func findMarketplace(value any, target string, host agenthost.ID) (nameFound, targetFound, valid bool) {
	var raw []any
	switch typed := value.(type) {
	case []any:
		raw = typed
	case map[string]any:
		values, ok := typed["marketplaces"].([]any)
		if !ok || host != agenthost.IDCodex {
			return false, false, false
		}
		raw = values
	default:
		return false, false, false
	}
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return false, false, false
		}
		if firstString(entry, "name", "marketplaceName") != managedMarketplaceName {
			continue
		}
		nameFound = true
		for _, field := range []string{"root", "path", "source", "installLocation"} {
			if value, ok := entry[field].(string); ok && sameCleanPath(value, target) {
				targetFound = true
			}
		}
	}
	return nameFound, targetFound, true
}

func sameCleanPath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	leftAbsolute, err := filepath.Abs(left)
	return err == nil && filepath.Clean(leftAbsolute) == filepath.Clean(right)
}

func componentInspection(inspection Inspection, component Component) (ComponentInspection, bool) {
	for _, candidate := range inspection.Components {
		if candidate.Component == component {
			return candidate, true
		}
	}
	return ComponentInspection{}, false
}

func nativeActions(request Request, inspection Inspection, adapter nativeAdapter, descriptor packageDescriptor) (actions, verification, rollback []Action, blockedReason string) {
	packageState, ok := componentInspection(inspection, adapter.kind)
	if !ok {
		return nil, nil, nil, "Native package inspection is missing from the bound plan."
	}
	verification = []Action{commandAction("verify_native_package", adapter.inspect)}
	sourceState := ComponentInspection{State: StateHealthy}
	stageState, stageOK := componentInspection(inspection, ComponentStage)
	if !stageOK {
		return nil, verification, nil, "Managed-package inspection is missing from the bound plan."
	}
	if adapter.sourceCheck != nil {
		var sourceOK bool
		sourceState, sourceOK = componentInspection(inspection, ComponentSource)
		if !sourceOK {
			return nil, verification, nil, "Package-source inspection is missing from the bound plan."
		}
		verification = append(verification, commandAction("verify_package_source", *adapter.sourceCheck))
	}
	for _, state := range []State{packageState.State, sourceState.State, stageState.State} {
		switch state {
		case StateUnavailable, StateUnreadable, StateMalformed, StateNameConflict:
			return nil, verification, nil, "The native package or its managed source cannot be changed safely."
		case StateAbsent, StateHealthy, StateDisabled, StateStalePath, StateVersionDrift:
		}
	}
	stage := Action{
		Kind: ActionPackage, Purpose: "stage_skill_package",
		Package: &PackageChange{
			Source: descriptor.sourceRoot, Target: descriptor.targetRoot,
			Version: descriptor.version, Kind: descriptor.kind, SourceSHA256: descriptor.sourceFingerprint,
			PreviousSHA256: stageState.Fingerprint,
		},
	}
	switch request.Operation {
	case OperationSetup, OperationRepair:
		if packageState.State == StateHealthy && sourceState.State == StateHealthy && stageState.State == StateHealthy {
			return nil, verification, nil, ""
		}
		actions = append(actions, stage)
		if packageState.State != StateAbsent {
			actions = append(actions, commandAction("remove_stale_native_package", adapter.remove(request, descriptor)))
		}
		if adapter.addSource != nil && sourceState.State == StateAbsent {
			actions = append(actions, commandAction("register_private_package_source", adapter.addSource(request, descriptor)))
		}
		actions = append(actions, commandAction("install_native_skill_package", adapter.install(request, descriptor)))
		rollback = []Action{commandAction("remove_native_skill_package", adapter.remove(request, descriptor))}
	case OperationRemove:
		if packageState.State != StateAbsent {
			actions = append(actions, commandAction("remove_native_skill_package", adapter.remove(request, descriptor)))
		}
		if adapter.removeSource != nil && sourceState.State != StateAbsent {
			actions = append(actions, commandAction("remove_private_package_source", adapter.removeSource(request, descriptor)))
		}
		if stageState.State != StateAbsent {
			removeStage := stage
			removeStage.Purpose = "remove_staged_skill_package"
			removeStage.Package.Remove = true
			actions = append(actions, removeStage)
		}
	}
	return actions, verification, rollback, ""
}

func packageActionMatches(change *PackageChange, descriptor packageDescriptor) error {
	if change == nil || change.Source != descriptor.sourceRoot || change.Target != descriptor.targetRoot ||
		change.Version != descriptor.version || change.Kind != descriptor.kind ||
		change.SourceSHA256 != descriptor.sourceFingerprint || change.PreviousSHA256 == "" {
		return errors.New("package action does not match the reviewed package source and target")
	}
	return nil
}
