package integrationlifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"unicode"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

const maximumInspectionBytes = 64 << 10

// Execution is bounded process evidence. Output is used only in memory to
// classify one named registration and is never copied into plans or results.
type Execution struct {
	Started   bool
	ExitCode  int
	Output    []byte
	Truncated bool
}

type Executor interface {
	Run(context.Context, Command, int64) (Execution, error)
}

type Engine struct {
	Catalog     agenthost.Catalog
	Executor    Executor
	Environment Environment
	JSONStore   *JSONStore
}

func (engine Engine) Inspect(ctx context.Context, request Request) (Inspection, error) {
	if err := request.Validate(); err != nil {
		return Inspection{}, err
	}
	inspection, err := engine.inspectMCP(ctx, request)
	if err != nil {
		return Inspection{}, err
	}
	inspection.Components = []ComponentInspection{{
		Component: ComponentMCP, State: inspection.State, Fingerprint: inspection.Fingerprint, Detail: inspection.Detail,
	}}
	adapter, descriptor, nativeOK, err := resolveNativeAdapter(engine.Environment, request)
	if err != nil {
		if !nativeOK {
			return Inspection{}, err
		}
		component := ComponentInspection{
			Component: adapter.kind, State: StateUnreadable,
			Detail: "The reviewed native package source is unsafe or unreadable.",
		}
		inspection.Components = append(inspection.Components, component)
		inspection.Fingerprint = combinedInspectionFingerprint(
			inspection.Fingerprint, string(component.State), string(component.Component),
		)
	}
	if nativeOK && err == nil {
		native, inspectErr := engine.inspectNative(ctx, request, adapter, descriptor)
		if inspectErr != nil {
			return Inspection{}, inspectErr
		}
		inspection.Components = append(inspection.Components, native.components...)
		inspection.Fingerprint = combinedInspectionFingerprint(inspection.Fingerprint, native.fingerprint)
	}
	skill, skillOK, err := resolveSkillDescriptor(engine.Environment, request)
	if err != nil {
		if !skillOK {
			return Inspection{}, err
		}
		component := ComponentInspection{
			Component: ComponentSkill, State: StateUnreadable,
			Detail: "The documented portable Skill path or source is unsafe or unreadable.",
		}
		inspection.Components = append(inspection.Components, component)
		inspection.Fingerprint = combinedInspectionFingerprint(
			inspection.Fingerprint, string(component.State), string(component.Component),
		)
	}
	if skillOK && err == nil {
		component, inspectErr := (SkillStore{}).Inspect(skill)
		if inspectErr != nil {
			return Inspection{}, inspectErr
		}
		inspection.Components = append(inspection.Components, component)
		inspection.Fingerprint = combinedInspectionFingerprint(inspection.Fingerprint, component.Fingerprint)
	}
	inspection.State, inspection.Detail = combinedInspectionState(inspection.Components)
	return inspection, nil
}

func (engine Engine) inspectMCP(ctx context.Context, request Request) (Inspection, error) {
	_, inspect, _, list, ok, err := OfficialCommands(request)
	if err != nil {
		return Inspection{}, err
	}
	if ok && engine.Executor != nil {
		execution, runErr := engine.Executor.Run(ctx, inspect, maximumInspectionBytes)
		if runErr != nil {
			return Inspection{}, fmt.Errorf("inspect %s integration: %w", request.Host, runErr)
		}
		return classifyCommandInspection(request, execution, list), nil
	}
	adapter, path, fileOK, err := resolveJSONAdapter(engine.Environment, request)
	if err != nil {
		return Inspection{}, err
	}
	if fileOK {
		store := engine.JSONStore
		if store == nil {
			store = &JSONStore{}
		}
		return store.Inspect(path, adapter, request, engine.Environment)
	}
	yamlPath, yamlOK, err := resolveYAMLAdapter(engine.Environment, request)
	if err != nil {
		return Inspection{}, err
	}
	if yamlOK {
		return (YAMLStore{}).Inspect(yamlPath, request, engine.Environment)
	}
	if !ok {
		return Inspection{
			State: StateUnavailable, Scope: request.Scope,
			Detail: "No reviewed lifecycle adapter is available for this host and scope.",
		}, nil
	}
	return Inspection{
		State: StateUnavailable, Scope: request.Scope,
		Detail: "The reviewed host command cannot be executed in this runtime.",
	}, nil
}

func combinedInspectionFingerprint(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func combinedInspectionState(components []ComponentInspection) (State, string) {
	active := make([]ComponentInspection, 0, len(components))
	for _, component := range components {
		if component.Component != ComponentSource && component.Component != ComponentStage {
			active = append(active, component)
		}
	}
	for _, state := range []State{StateNameConflict, StateMalformed, StateUnreadable, StateUnavailable} {
		for _, component := range components {
			if component.State == state {
				return state, component.Detail
			}
		}
	}
	allAbsent := len(active) > 0
	allHealthy := len(active) > 0
	for _, component := range active {
		allAbsent = allAbsent && component.State == StateAbsent
		allHealthy = allHealthy && component.State == StateHealthy
		if component.State == StateDisabled {
			return StateDisabled, "One or more Corresync integration components are disabled."
		}
		if component.State == StateStalePath {
			return StateStalePath, "One or more Corresync launch paths are stale."
		}
		if component.State == StateVersionDrift {
			return StateVersionDrift, "The installed Corresync integration package version is stale."
		}
	}
	if allAbsent {
		return StateAbsent, "The Corresync integration is not installed."
	}
	if allHealthy {
		for _, component := range components {
			if (component.Component == ComponentSource || component.Component == ComponentStage) &&
				component.State == StateAbsent {
				return StateVersionDrift, "The integration is active but its managed package source needs repair."
			}
		}
		return StateHealthy, "All reviewed Corresync integration components are healthy."
	}
	return StateVersionDrift, "The Corresync integration is only partially installed."
}

func classifyCommandInspection(request Request, execution Execution, list bool) Inspection {
	inspection := Inspection{Scope: request.Scope}
	output := strings.TrimSpace(string(execution.Output))
	inspection.Fingerprint = inspectionFingerprint(execution, output)
	if execution.Truncated {
		inspection.State = StateMalformed
		inspection.Detail = "The host integration inventory exceeded the bounded parser limit."
		return inspection
	}
	lower := strings.ToLower(output)
	if !execution.Started {
		inspection.State = StateUnavailable
		inspection.Detail = "The reviewed host command is not installed or cannot be started."
		return inspection
	}
	if execution.ExitCode != 0 {
		switch {
		case containsAny(lower, "permission denied", "access denied", "not readable"):
			inspection.State = StateUnreadable
			inspection.Detail = "The host command could not read its integration configuration."
		case containsAny(lower, "malformed", "parse error", "invalid config", "invalid json", "invalid toml"):
			inspection.State = StateMalformed
			inspection.Detail = "The host rejected its integration configuration as malformed."
		default:
			inspection.State = StateAbsent
			inspection.Detail = "The named Corresync integration is not registered."
		}
		return inspection
	}
	if list {
		record, found, valid := selectNamedCommandRecord(output, request.ServerName)
		if !valid {
			inspection.State = StateMalformed
			inspection.Detail = "The host returned an ambiguous or malformed integration inventory."
			return inspection
		}
		if !found {
			inspection.State = StateAbsent
			inspection.Detail = "The named Corresync integration is not registered."
			return inspection
		}
		output = record
		lower = strings.ToLower(record)
	}
	if state, handled := classifyJSONCommandRecord(output, request); handled {
		inspection.State = state
		switch state {
		case StateHealthy:
			inspection.Detail = "The host reports the expected absolute Corresync launch contract."
		case StateDisabled:
			inspection.Detail = "The named Corresync integration is present but disabled."
		case StateStalePath:
			inspection.Detail = "The named Corresync integration uses stale launch fields."
		case StateNameConflict, StateAbsent, StateVersionDrift, StateMalformed, StateUnreadable, StateUnavailable:
			inspection.Detail = "The requested name belongs to a different host integration."
		}
		return inspection
	}
	staleCommand := !containsExactTextValue(output, request.Executable)
	if staleCommand && !outputMentionsOwnedExecutable(output) {
		inspection.State = StateNameConflict
		inspection.Detail = "The requested name belongs to a different host integration."
		return inspection
	}
	if containsExactTextValue(lower, "disabled") || containsAny(lower, "enabled: false", `"enabled":false`, `"enabled": false`) {
		inspection.State = StateDisabled
		inspection.Detail = "The named Corresync integration is present but disabled."
		return inspection
	}
	if staleCommand {
		inspection.State = StateStalePath
		inspection.Detail = "The named Corresync integration uses a different executable path."
		return inspection
	}
	if containsAny(lower, "alwaysallow", "always_allow", "always-allow", "autoapprove", "auto_approve", "auto-approve") {
		inspection.State = StateStalePath
		inspection.Detail = "The named Corresync integration contains a host auto-approval setting."
		return inspection
	}
	for _, argument := range request.Arguments {
		if argument != "" && !containsRenderedArgument(output, argument) {
			inspection.State = StateStalePath
			inspection.Detail = "The named Corresync integration uses stale launch arguments."
			return inspection
		}
	}
	inspection.State = StateHealthy
	inspection.Detail = "The host reports the expected absolute Corresync launch contract."
	return inspection
}

func containsRenderedArgument(output, argument string) bool {
	if containsExactTextValue(output, argument) {
		return true
	}
	escaped := strings.NewReplacer(
		`\`, `\\`,
		" ", `\ `,
		"\t", `\`+"\t",
	).Replace(argument)
	return escaped != argument && containsExactTextValue(output, escaped)
}

func selectNamedCommandRecord(output, name string) (record string, found, valid bool) {
	var document any
	if json.Unmarshal([]byte(output), &document) == nil {
		entry, found, valid := selectNamedJSONEntry(document, name)
		if !valid || !found {
			return "", found, valid
		}
		encoded, err := json.Marshal(entry)
		return string(encoded), true, err == nil
	}
	lines := strings.Split(output, "\n")
	match := -1
	for index, line := range lines {
		if !containsName(line, name) {
			continue
		}
		if match >= 0 {
			return "", false, false
		}
		match = index
	}
	if match < 0 {
		return "", false, true
	}
	selected := []string{lines[match]}
	indent := leadingWhitespace(lines[match])
	for index := match + 1; index < len(lines); index++ {
		line := lines[index]
		if strings.TrimSpace(line) == "" {
			break
		}
		if leadingWhitespace(line) <= indent {
			break
		}
		selected = append(selected, line)
	}
	return strings.Join(selected, "\n"), true, true
}

func selectNamedJSONEntry(document any, name string) (any, bool, bool) {
	if values, ok := document.([]any); ok {
		return namedEntryFromArray(values, name)
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, false, false
	}
	for _, key := range []string{"mcpServers", "servers"} {
		if raw, exists := root[key]; exists {
			entries, ok := raw.(map[string]any)
			if !ok {
				return nil, false, false
			}
			entry, found := entries[name]
			return entry, found, true
		}
	}
	if entry, exists := root[name]; exists {
		return entry, true, true
	}
	for _, key := range []string{"items", "entries"} {
		if raw, exists := root[key]; exists {
			entries, ok := raw.([]any)
			if !ok {
				return nil, false, false
			}
			return namedEntryFromArray(entries, name)
		}
	}
	return nil, false, false
}

func namedEntryFromArray(entries []any, name string) (any, bool, bool) {
	var selected any
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, false, false
		}
		if firstString(entry, "name", "id") != name {
			continue
		}
		if selected != nil {
			return nil, false, false
		}
		selected = entry
	}
	return selected, selected != nil, true
}

func classifyJSONCommandRecord(output string, request Request) (State, bool) {
	var entry map[string]any
	if json.Unmarshal([]byte(output), &entry) != nil || entry == nil {
		return "", false
	}
	command, ok := entry["command"].(string)
	if !ok {
		return StateNameConflict, true
	}
	staleCommand := command != request.Executable
	if staleCommand && !ownedExecutable(command) {
		return StateNameConflict, true
	}
	if enabled, ok := entry["enabled"].(bool); ok && !enabled {
		return StateDisabled, true
	}
	if staleCommand {
		return StateStalePath, true
	}
	if hasHostAutoApproval(entry) {
		return StateStalePath, true
	}
	if !equalStringArrayValue(entry["args"], request.Arguments) {
		return StateStalePath, true
	}
	return StateHealthy, true
}

func leadingWhitespace(value string) int {
	return len(value) - len(strings.TrimLeft(value, " \t"))
}

func containsExactTextValue(output, value string) bool {
	if value == "" {
		return true
	}
	for offset := 0; ; {
		index := strings.Index(output[offset:], value)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || !textValueRune(output[index-1])
		after := index + len(value)
		afterOK := after == len(output) || !textValueRune(output[after])
		if beforeOK && afterOK {
			return true
		}
		offset = index + 1
	}
}

func textValueRune(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' ||
		strings.ContainsRune("_./\\-", rune(value))
}

func outputMentionsOwnedExecutable(output string) bool {
	for _, field := range strings.Fields(output) {
		candidate := strings.Trim(field, "\"'`,;:[]{}()")
		if strings.ContainsAny(candidate, `/\\`) && ownedExecutable(candidate) {
			return true
		}
	}
	return false
}

func inspectionFingerprint(execution Execution, output string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%t\x00%d\x00%t\x00%s", execution.Started, execution.ExitCode, execution.Truncated, output)))
	return hex.EncodeToString(sum[:])
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}

func containsName(output, name string) bool {
	for _, field := range strings.FieldsFunc(output, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}) {
		if field == name {
			return true
		}
	}
	return false
}

func (engine Engine) Plan(ctx context.Context, request Request) (Plan, error) {
	previous, err := engine.Inspect(ctx, request)
	if err != nil {
		return Plan{}, err
	}
	return engine.planFromInspection(request, previous)
}

func (engine Engine) planFromInspection(request Request, previous Inspection) (Plan, error) {
	if err := request.Validate(); err != nil {
		return Plan{}, err
	}
	host, ok := engine.Catalog.Lookup(string(request.Host))
	if !ok {
		return Plan{}, fmt.Errorf("unknown integration host %q", request.Host)
	}
	add, inspect, remove, _, commandOK, err := OfficialCommands(request)
	if err != nil {
		return Plan{}, err
	}
	_, path, fileOK, err := resolveJSONAdapter(engine.Environment, request)
	if err != nil {
		return Plan{}, err
	}
	yamlPath, yamlOK, err := resolveYAMLAdapter(engine.Environment, request)
	if err != nil {
		return Plan{}, err
	}
	fileKind := ActionJSON
	if yamlOK {
		path = yamlPath
		fileKind = ActionYAML
	}
	plan := Plan{
		SchemaVersion: SchemaVersion, RequestBinding: requestBinding(request), Operation: request.Operation,
		Host: request.Host, DisplayName: host.DisplayName, Scope: request.Scope,
		ServerName: request.ServerName, Components: []Component{ComponentMCP},
		Previous: previous, ReloadRequired: host.Lifecycle.ReloadRequired,
	}
	mcpPrevious := previous
	if component, ok := componentInspection(previous, ComponentMCP); ok {
		mcpPrevious.State = component.State
		mcpPrevious.Detail = component.Detail
	}
	if commandOK {
		plan.Verification = []Action{commandAction("verify_registration", inspect)}
	} else if fileOK || yamlOK {
		plan.Verification = []Action{fileAction(fileKind, "verify_registration", path, request.ServerName)}
	}
	switch mcpPrevious.State {
	case StateUnavailable, StateMalformed, StateUnreadable:
		plan.Blocked = true
		plan.Reason = mcpPrevious.Detail
		return plan, nil
	case StateNameConflict:
		plan.Blocked = true
		plan.Reason = "The requested name is not Corresync-owned; choose another name or remove it in the host."
		return plan, nil
	case StateAbsent, StateHealthy, StateDisabled, StateStalePath, StateVersionDrift:
	}
	if !commandOK && !fileOK && !yamlOK {
		plan.Blocked = true
		plan.Reason = "No reviewed lifecycle adapter is available for this host and scope."
		return plan, nil
	}
	if blocked, ok := blockingNonMCPComponent(previous.Components); ok {
		for _, component := range previous.Components {
			if component.Component != ComponentMCP && !slices.Contains(plan.Components, component.Component) {
				plan.Components = append(plan.Components, component.Component)
			}
		}
		plan.Blocked = true
		plan.Reason = blocked.Detail
		return plan, nil
	}
	switch request.Operation {
	case OperationSetup:
		switch mcpPrevious.State {
		case StateAbsent:
			if commandOK {
				plan.Actions = []Action{commandAction("register_corresync", add)}
				plan.Rollback = []Action{commandAction("remove_corresync", remove)}
			} else {
				plan.Actions = []Action{fileAction(fileKind, "register_corresync", path, request.ServerName)}
				plan.Rollback = []Action{fileAction(fileKind, "remove_corresync", path, request.ServerName)}
			}
		case StateHealthy:
		case StateDisabled, StateStalePath, StateVersionDrift:
			if commandOK {
				plan.Actions = []Action{
					commandAction("remove_stale_corresync", remove),
					commandAction("register_corresync", add),
				}
				plan.Rollback = []Action{commandAction("remove_corresync", remove)}
			} else {
				plan.Actions = []Action{fileAction(fileKind, "repair_corresync", path, request.ServerName)}
			}
		case StateNameConflict, StateMalformed, StateUnreadable, StateUnavailable:
			return Plan{}, errors.New("unsafe MCP state reached setup planning")
		}
	case OperationRepair:
		switch mcpPrevious.State {
		case StateAbsent, StateHealthy:
		case StateDisabled, StateStalePath, StateVersionDrift:
			if commandOK {
				plan.Actions = []Action{
					commandAction("remove_stale_corresync", remove),
					commandAction("register_corresync", add),
				}
				plan.Rollback = []Action{commandAction("remove_corresync", remove)}
			} else {
				plan.Actions = []Action{fileAction(fileKind, "repair_corresync", path, request.ServerName)}
			}
		case StateNameConflict, StateMalformed, StateUnreadable, StateUnavailable:
			return Plan{}, errors.New("unsafe MCP state reached repair planning")
		}
	case OperationRemove:
		if mcpPrevious.State != StateAbsent {
			if commandOK {
				plan.Actions = []Action{commandAction("remove_corresync", remove)}
			} else {
				plan.Actions = []Action{fileAction(fileKind, "remove_corresync", path, request.ServerName)}
			}
		}
		if commandOK {
			plan.Verification = []Action{commandAction("verify_absence", inspect)}
		} else {
			plan.Verification = []Action{fileAction(fileKind, "verify_absence", path, request.ServerName)}
		}
	}
	adapter, descriptor, nativeOK, err := resolveNativeAdapter(engine.Environment, request)
	if err != nil {
		return Plan{}, err
	}
	if nativeOK {
		plan.Components = append(plan.Components, descriptor.kind, ComponentStage)
		if adapter.sourceCheck != nil {
			plan.Components = append(plan.Components, ComponentSource)
		}
		actions, verification, rollback, blockedReason := nativeActions(request, previous, adapter, descriptor)
		if blockedReason != "" {
			plan.Blocked = true
			plan.Reason = blockedReason
			plan.Actions = nil
			plan.Rollback = nil
			return plan, nil
		}
		plan.Actions = append(plan.Actions, actions...)
		plan.Verification = append(plan.Verification, verification...)
		plan.Rollback = append(plan.Rollback, rollback...)
	}
	skill, skillOK, err := resolveSkillDescriptor(engine.Environment, request)
	if err != nil {
		return Plan{}, err
	}
	if skillOK {
		plan.Components = append(plan.Components, ComponentSkill)
		actions, verification, rollback, blockedReason := skillActions(request, previous, skill)
		if blockedReason != "" {
			plan.Blocked = true
			plan.Reason = blockedReason
			plan.Actions = nil
			plan.Rollback = nil
			return plan, nil
		}
		plan.Actions = append(plan.Actions, actions...)
		plan.Verification = append(plan.Verification, verification...)
		plan.Rollback = append(plan.Rollback, rollback...)
	}
	return plan, nil
}

func blockingNonMCPComponent(components []ComponentInspection) (ComponentInspection, bool) {
	for _, component := range components {
		if component.Component == ComponentMCP {
			continue
		}
		switch component.State {
		case StateNameConflict, StateMalformed, StateUnreadable, StateUnavailable:
			return component, true
		case StateAbsent, StateHealthy, StateDisabled, StateStalePath, StateVersionDrift:
		}
	}
	return ComponentInspection{}, false
}

func fileAction(kind ActionKind, purpose, path, entry string) Action {
	return Action{
		Kind: kind, Purpose: purpose,
		File: &FileChange{Path: path, Entry: entry, Normalization: true},
	}
}

func (engine Engine) Apply(ctx context.Context, request Request, plan Plan) (Result, error) {
	if err := validatePlanBinding(request, plan); err != nil {
		return Result{}, err
	}
	expectedPlan, err := engine.planFromInspection(request, plan.Previous)
	if err != nil {
		return Result{}, err
	}
	if !reflect.DeepEqual(plan, expectedPlan) {
		return Result{}, errors.New("integration plan actions do not match the reviewed adapter plan")
	}
	if plan.Blocked {
		return Result{Host: request.Host, Status: ResultBlocked, Message: plan.Reason}, nil
	}
	current, err := engine.Inspect(ctx, request)
	if err != nil {
		return Result{}, err
	}
	if current.State != plan.Previous.State || current.Fingerprint != plan.Previous.Fingerprint {
		return Result{
			Host: request.Host, Status: ResultFailedPreserved,
			Message: "Host integration state changed after preview; no change was applied.",
		}, nil
	}
	if len(plan.Actions) == 0 {
		status := ResultAlreadyCurrent
		message := "The integration is already current."
		if request.Operation == OperationRemove || request.Operation == OperationRepair && plan.Previous.State == StateAbsent {
			status = ResultAlreadyAbsent
			message = "The integration is already absent."
		}
		return Result{Host: request.Host, Status: status, Verified: true, Message: message}, nil
	}
	changed := false
	for _, action := range plan.Actions {
		var actionErr error
		switch action.Kind {
		case ActionCommand:
			if action.Command == nil || engine.Executor == nil {
				return Result{}, errors.New("command action is missing its executor or argv")
			}
			execution, runErr := engine.Executor.Run(ctx, *action.Command, maximumInspectionBytes)
			if runErr != nil {
				actionErr = runErr
			} else if !execution.Started || execution.ExitCode != 0 {
				actionErr = errors.New("host command reported failure")
			}
		case ActionJSON:
			if action.File == nil {
				return Result{}, errors.New("JSON action is missing its file target")
			}
			adapter, path, ok, resolveErr := resolveJSONAdapter(engine.Environment, request)
			switch {
			case resolveErr != nil:
				actionErr = resolveErr
			case !ok || path != action.File.Path || action.File.Entry != request.ServerName:
				actionErr = errors.New("JSON action does not match the reviewed adapter target")
			default:
				store := engine.JSONStore
				if store == nil {
					store = &JSONStore{}
				}
				actionErr = store.Apply(
					ctx, path, adapter, request, engine.Environment, mcpInspectionFingerprint(plan.Previous),
					request.Operation == OperationRemove,
				)
			}
		case ActionYAML:
			if action.File == nil {
				return Result{}, errors.New("YAML action is missing its file target")
			}
			path, ok, resolveErr := resolveYAMLAdapter(engine.Environment, request)
			switch {
			case resolveErr != nil:
				actionErr = resolveErr
			case !ok || path != action.File.Path || action.File.Entry != request.ServerName:
				actionErr = errors.New("YAML action does not match the reviewed adapter target")
			default:
				actionErr = (YAMLStore{}).Apply(
					ctx, path, request, engine.Environment, mcpInspectionFingerprint(plan.Previous),
					request.Operation == OperationRemove,
				)
			}
		case ActionPackage:
			_, descriptor, ok, resolveErr := resolveNativeAdapter(engine.Environment, request)
			bindingErr := packageActionMatches(action.Package, descriptor)
			switch {
			case resolveErr != nil:
				actionErr = resolveErr
			case !ok:
				actionErr = errors.New("package action has no reviewed native adapter")
			case bindingErr != nil:
				actionErr = bindingErr
			case action.Package.Remove:
				actionErr = (PackageStore{}).Remove(ctx, descriptor, action.Package.PreviousSHA256)
			default:
				actionErr = (PackageStore{}).Stage(
					ctx, descriptor, action.Package.SourceSHA256, action.Package.PreviousSHA256,
				)
			}
		case ActionSkill:
			descriptor, ok, resolveErr := resolveSkillDescriptor(engine.Environment, request)
			bindingErr := skillActionMatches(action.Package, descriptor)
			switch {
			case resolveErr != nil:
				actionErr = resolveErr
			case !ok:
				actionErr = errors.New("skill action has no reviewed portable target")
			case bindingErr != nil:
				actionErr = bindingErr
			case action.Package.Remove:
				actionErr = (SkillStore{}).Remove(ctx, descriptor, action.Package.PreviousSHA256)
			default:
				actionErr = (SkillStore{}).Install(ctx, descriptor, action.Package.PreviousSHA256)
			}
		default:
			return Result{}, errors.New("unsupported integration plan action")
		}
		if actionErr != nil {
			status := ResultFailedPreserved
			if changed {
				status = ResultFailedChanged
			}
			return Result{
				Host: request.Host, Status: status, Changed: changed,
				Message: failureMessage(changed, action.Purpose),
			}, actionErr
		}
		changed = true
	}
	verified, err := engine.Inspect(ctx, request)
	if err != nil {
		return Result{Host: request.Host, Status: ResultFailedChanged, Changed: true, Message: "The change was applied but verification failed."}, err
	}
	want := StateHealthy
	if request.Operation == OperationRemove {
		want = StateAbsent
	}
	if verified.State != want {
		return Result{
			Host: request.Host, Status: ResultFailedChanged, Changed: true,
			Message: fmt.Sprintf("The change was applied but verification reported %s.", verified.State),
		}, nil
	}
	status := ResultAppliedVerified
	message := "The integration was applied and verified."
	if plan.ReloadRequired {
		status = ResultReloadRequired
		message = "The integration was applied and verified; start a new host session or reload it."
	}
	return Result{Host: request.Host, Status: status, Changed: true, Verified: true, Message: message}, nil
}

func mcpInspectionFingerprint(inspection Inspection) string {
	for _, component := range inspection.Components {
		if component.Component == ComponentMCP && component.Fingerprint != "" {
			return component.Fingerprint
		}
	}
	return inspection.Fingerprint
}

func validatePlanBinding(request Request, plan Plan) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if plan.SchemaVersion != SchemaVersion || plan.Operation != request.Operation ||
		plan.Host != request.Host || plan.Scope != request.Scope || plan.ServerName != request.ServerName ||
		plan.RequestBinding != requestBinding(request) {
		return errors.New("integration plan does not match the requested operation")
	}
	return nil
}

func requestBinding(request Request) string {
	hash := sha256.New()
	for _, value := range []string{
		string(request.Operation), string(request.Host), string(request.Scope), request.ServerName,
		request.Executable, request.ProjectDirectory,
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, argument := range request.Arguments {
		_, _ = hash.Write([]byte(argument))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func failureMessage(changed bool, purpose string) string {
	if changed {
		return fmt.Sprintf("The %s step failed after an earlier change; rerun doctor for recovery guidance.", purpose)
	}
	return fmt.Sprintf("The %s step failed before host state changed.", purpose)
}

func ownedExecutable(value string) bool {
	base := strings.ToLower(filepath.Base(value))
	return base == "corr" || base == "corr.exe" || base == "corresync" || base == "corresync.exe"
}
