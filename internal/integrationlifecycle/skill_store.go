package integrationlifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/filelock"
)

const skillMarkerName = ".corresync-skill.json"

type skillDescriptor struct {
	host              agenthost.ID
	sourceRoot        string
	reviewedRoot      string
	targetRoot        string
	directoryMode     os.FileMode
	version           string
	sourceFingerprint string
	data              []byte
}

type skillMarker struct {
	SchemaVersion int            `json:"schemaVersion"`
	Version       string         `json:"version"`
	SourceSHA256  string         `json:"sourceSha256"`
	Hosts         []agenthost.ID `json:"hosts"`
}

type SkillStore struct{}

func resolveSkillDescriptor(environment Environment, request Request) (skillDescriptor, bool, error) {
	if environment.BundleDirectory == "" {
		return skillDescriptor{}, false, nil
	}
	descriptor := skillDescriptor{host: request.Host, sourceRoot: environment.BundleDirectory}
	//nolint:exhaustive // Only hosts with an official portable Skill location are supported.
	switch request.Host {
	case agenthost.IDVSCode:
		//nolint:exhaustive // Other scopes have no documented VS Code Skill location.
		switch request.Scope {
		case agenthost.ScopeUser:
			descriptor.reviewedRoot = environment.HomeDirectory
			descriptor.targetRoot = filepath.Join(environment.HomeDirectory, ".copilot", "skills", "corresync")
		case agenthost.ScopeWorkspace:
			descriptor.reviewedRoot = request.ProjectDirectory
			descriptor.directoryMode = 0o755
			descriptor.targetRoot = filepath.Join(request.ProjectDirectory, ".github", "skills", "corresync")
		}
	case agenthost.IDOpenCode:
		//nolint:exhaustive // Other scopes have no documented OpenCode Skill location.
		switch request.Scope {
		case agenthost.ScopeUser:
			descriptor.reviewedRoot = environment.ConfigDirectory
			descriptor.targetRoot = filepath.Join(environment.ConfigDirectory, "opencode", "skills", "corresync")
		case agenthost.ScopeProject:
			descriptor.reviewedRoot = request.ProjectDirectory
			descriptor.directoryMode = 0o755
			descriptor.targetRoot = filepath.Join(request.ProjectDirectory, ".opencode", "skills", "corresync")
		}
	case agenthost.IDCline:
		if request.Scope == agenthost.ScopeUser {
			descriptor.reviewedRoot = environment.HomeDirectory
			descriptor.targetRoot = filepath.Join(environment.HomeDirectory, ".cline", "skills", "corresync")
		}
	case agenthost.IDZed:
		//nolint:exhaustive // Other scopes have no documented Zed Skill location.
		switch request.Scope {
		case agenthost.ScopeUser:
			descriptor.reviewedRoot = environment.HomeDirectory
			descriptor.targetRoot = filepath.Join(environment.HomeDirectory, ".agents", "skills", "corresync")
		case agenthost.ScopeProject:
			descriptor.reviewedRoot = request.ProjectDirectory
			descriptor.directoryMode = 0o755
			descriptor.targetRoot = filepath.Join(request.ProjectDirectory, ".agents", "skills", "corresync")
		}
	}
	if descriptor.targetRoot == "" {
		return skillDescriptor{}, false, nil
	}
	if descriptor.directoryMode == 0 {
		descriptor.directoryMode = 0o700
	}
	if err := validateBundleRoot(environment.BundleDirectory); err != nil {
		return descriptor, true, err
	}
	if err := validateTargetParents(filepath.Join(descriptor.targetRoot, "SKILL.md"), request, environment); err != nil {
		return descriptor, true, err
	}
	data, err := readPackageSource(environment.BundleDirectory, "plugins/corresync/skills/corresync/SKILL.md")
	if err != nil {
		return descriptor, true, err
	}
	manifest, err := readPackageSource(environment.BundleDirectory, "plugins/corresync/.codex-plugin/plugin.json")
	if err != nil {
		return descriptor, true, err
	}
	var metadata struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(manifest, &metadata); err != nil || !packageVersionPattern.MatchString(metadata.Version) {
		return descriptor, true, errors.New("portable Skill source has an invalid package version")
	}
	descriptor.version = metadata.Version
	descriptor.data = data
	descriptor.sourceFingerprint = fileFingerprint(data)
	return descriptor, true, nil
}

func (SkillStore) Inspect(descriptor skillDescriptor) (ComponentInspection, error) {
	component := ComponentInspection{
		Component: ComponentSkill, ExpectedVersion: descriptor.version,
		Fingerprint: absentFileFingerprint(descriptor.targetRoot),
	}
	info, err := os.Lstat(descriptor.targetRoot)
	if errors.Is(err, os.ErrNotExist) {
		component.State = StateAbsent
		component.Detail = "The documented portable Corresync Skill is absent."
		return component, nil
	}
	if err != nil {
		component.State = StateUnreadable
		component.Detail = "The portable Skill directory is unreadable."
		return component, nil //nolint:nilerr // Filesystem failures are represented by the typed component state.
	}
	if !info.IsDir() || IsSymlinkOrReparsePoint(info) || !ownedByCurrentUser(info) || WritableByOtherUsers(info) {
		component.State = StateNameConflict
		component.Detail = "The portable Skill name belongs to an unmanaged directory."
		return component, nil
	}
	markerData, skillData, fingerprint, err := readSkillTree(descriptor.targetRoot)
	if err != nil {
		component.State = StateNameConflict
		component.Detail = "The portable Skill directory is not safely Corresync-owned."
		return component, nil //nolint:nilerr // Unsafe ownership is represented by the typed component state.
	}
	component.Fingerprint = fingerprint
	var marker skillMarker
	if err := json.Unmarshal(markerData, &marker); err != nil || marker.SchemaVersion != SchemaVersion ||
		!validSkillHosts(marker.Hosts) {
		component.State = StateNameConflict
		component.Detail = "The portable Skill ownership marker is invalid."
		return component, nil //nolint:nilerr // Invalid ownership is represented by the typed component state.
	}
	component.Version = marker.Version
	if !slices.Contains(marker.Hosts, descriptor.host) {
		component.State = StateAbsent
		component.Detail = "The shared portable Skill exists but is not registered for this host."
		return component, nil
	}
	if marker.Version != descriptor.version || marker.SourceSHA256 != descriptor.sourceFingerprint ||
		!bytes.Equal(skillData, descriptor.data) {
		component.State = StateVersionDrift
		component.Detail = "The portable Corresync Skill version is stale."
		return component, nil
	}
	component.State = StateHealthy
	component.Detail = "The host has the matching portable Corresync Skill."
	return component, nil
}

func (SkillStore) Install(ctx context.Context, descriptor skillDescriptor, expectedFingerprint string) (returnErr error) {
	if err := prepareSkillParent(descriptor); err != nil {
		return err
	}
	lock, err := filelock.AcquireSidecar(ctx, descriptor.targetRoot+".corresync.lock")
	if err != nil {
		return fmt.Errorf("acquire portable Skill lock: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	if err := validateSkillTarget(descriptor); err != nil {
		return err
	}
	inspection, err := (SkillStore{}).Inspect(descriptor)
	if err != nil {
		return err
	}
	if inspection.Fingerprint != expectedFingerprint {
		return errors.New("portable Skill changed after preview")
	}
	hosts := []agenthost.ID{descriptor.host}
	if inspection.State != StateAbsent || inspection.Fingerprint != absentFileFingerprint(descriptor.targetRoot) {
		markerData, _, _, readErr := readSkillTree(descriptor.targetRoot)
		if readErr != nil {
			return readErr
		}
		var marker skillMarker
		if err := json.Unmarshal(markerData, &marker); err != nil || !validSkillHosts(marker.Hosts) {
			return errors.New("portable Skill ownership marker is invalid")
		}
		hosts = append(slices.Clone(marker.Hosts), descriptor.host)
	}
	hosts = normalizeSkillHosts(hosts)
	return replaceSkillTree(descriptor, hosts, descriptor.version, descriptor.sourceFingerprint, descriptor.data)
}

func (SkillStore) Remove(ctx context.Context, descriptor skillDescriptor, expectedFingerprint string) (returnErr error) {
	if err := validateSkillTarget(descriptor); err != nil {
		return err
	}
	lock, err := filelock.AcquireSidecar(ctx, descriptor.targetRoot+".corresync.lock")
	if err != nil {
		return fmt.Errorf("acquire portable Skill lock: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	if err := validateSkillTarget(descriptor); err != nil {
		return err
	}
	inspection, err := (SkillStore{}).Inspect(descriptor)
	if err != nil {
		return err
	}
	if inspection.Fingerprint != expectedFingerprint {
		return errors.New("portable Skill changed after preview")
	}
	if inspection.State == StateAbsent {
		return nil
	}
	markerData, skillData, _, err := readSkillTree(descriptor.targetRoot)
	if err != nil {
		return err
	}
	var marker skillMarker
	if err := json.Unmarshal(markerData, &marker); err != nil || !validSkillHosts(marker.Hosts) {
		return errors.New("portable Skill ownership marker is invalid")
	}
	hosts := make([]agenthost.ID, 0, len(marker.Hosts)-1)
	for _, host := range marker.Hosts {
		if host != descriptor.host {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) > 0 {
		return replaceSkillTree(descriptor, hosts, marker.Version, marker.SourceSHA256, skillData)
	}
	return removeSkillTree(descriptor)
}

func prepareSkillParent(descriptor skillDescriptor) error {
	if err := validateSkillTarget(descriptor); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(descriptor.targetRoot), descriptor.directoryMode); err != nil {
		return err
	}
	return validateSkillTarget(descriptor)
}

func validateSkillTarget(descriptor skillDescriptor) error {
	if !filepath.IsAbs(descriptor.reviewedRoot) || !filepath.IsAbs(descriptor.targetRoot) ||
		filepath.Clean(descriptor.reviewedRoot) != descriptor.reviewedRoot || filepath.Clean(descriptor.targetRoot) != descriptor.targetRoot {
		return errors.New("portable Skill root and target must be clean and absolute")
	}
	relative, err := filepath.Rel(descriptor.reviewedRoot, descriptor.targetRoot)
	if err != nil || relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("portable Skill target escapes its reviewed root")
	}
	for directory := filepath.Dir(descriptor.targetRoot); ; directory = filepath.Dir(directory) {
		info, statErr := os.Lstat(directory)
		if statErr == nil && (!info.IsDir() || IsSymlinkOrReparsePoint(info) || !ownedByCurrentUser(info) || WritableByOtherUsers(info)) {
			return fmt.Errorf("portable Skill parent has an unsafe type, owner, or mode: %s", directory)
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if directory == descriptor.reviewedRoot {
			break
		}
	}
	return nil
}

func readSkillTree(root string) (marker, skill []byte, fingerprint string, err error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || IsSymlinkOrReparsePoint(rootInfo) ||
		!ownedByCurrentUser(rootInfo) || WritableByOtherUsers(rootInfo) {
		return nil, nil, "", errors.Join(err, errors.New("portable Skill root is unsafe"))
	}
	expected := map[string]bool{skillMarkerName: true, "SKILL.md": true}
	values := make(map[string][]byte, len(expected))
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			return errors.New("portable Skill directory contains an unexpected directory")
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil || !expected[filepath.ToSlash(relative)] {
			return errors.New("portable Skill directory contains an unexpected file")
		}
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() || IsSymlinkOrReparsePoint(info) ||
			!ownedByCurrentUser(info) || WritableByOtherUsers(info) || info.Size() > maximumPackageFileSize {
			return errors.New("portable Skill directory contains an unsafe file")
		}
		data, readErr := readOpenedBounded(path, info, maximumPackageFileSize)
		if readErr != nil {
			return readErr
		}
		values[filepath.ToSlash(relative)] = data
		return nil
	})
	if err != nil || len(values) != len(expected) {
		return nil, nil, "", errors.Join(err, errors.New("portable Skill directory is incomplete"))
	}
	combined := append(slices.Clone(values[skillMarkerName]), 0)
	combined = append(combined, values["SKILL.md"]...)
	return values[skillMarkerName], values["SKILL.md"], fileFingerprint(combined), nil
}

func replaceSkillTree(descriptor skillDescriptor, hosts []agenthost.ID, version, sourceFingerprint string, skill []byte) error {
	if err := validateSkillTarget(descriptor); err != nil {
		return err
	}
	parent := filepath.Dir(descriptor.targetRoot)
	if err := os.MkdirAll(parent, descriptor.directoryMode); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".corresync-skill-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	if err := os.Chmod(temporary, descriptor.directoryMode); err != nil {
		return err
	}
	marker, err := json.MarshalIndent(skillMarker{
		SchemaVersion: SchemaVersion, Version: version, SourceSHA256: sourceFingerprint,
		Hosts: normalizeSkillHosts(hosts),
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := writePackageFile(temporary, skillMarkerName, append(marker, '\n')); err != nil {
		return err
	}
	if err := writePackageFile(temporary, "SKILL.md", skill); err != nil {
		return err
	}
	if err := syncPackageTree(temporary); err != nil {
		return err
	}
	if err := validateSkillTarget(descriptor); err != nil {
		return err
	}
	backup := descriptor.targetRoot + ".corresync.bak"
	if _, err := os.Lstat(backup); err == nil {
		if err := requireManagedSkill(backup); err != nil {
			return err
		}
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previous := false
	if _, err := os.Lstat(descriptor.targetRoot); err == nil {
		if err := requireManagedSkill(descriptor.targetRoot); err != nil {
			return err
		}
		if err := os.Rename(descriptor.targetRoot, backup); err != nil {
			return err
		}
		previous = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporary, descriptor.targetRoot); err != nil {
		if previous {
			_ = os.Rename(backup, descriptor.targetRoot)
		}
		return err
	}
	return syncDirectory(parent)
}

func removeSkillTree(descriptor skillDescriptor) error {
	if err := validateSkillTarget(descriptor); err != nil {
		return err
	}
	root := descriptor.targetRoot
	if err := requireManagedSkill(root); err != nil {
		return err
	}
	backup := root + ".corresync.bak"
	if _, err := os.Lstat(backup); err == nil {
		if err := requireManagedSkill(backup); err != nil {
			return err
		}
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(root, backup); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(root))
}

func requireManagedSkill(root string) error {
	marker, _, _, err := readSkillTree(root)
	if err != nil {
		return err
	}
	var value skillMarker
	if err := json.Unmarshal(marker, &value); err != nil || value.SchemaVersion != SchemaVersion || !validSkillHosts(value.Hosts) {
		return errors.New("portable Skill is not Corresync-owned")
	}
	return nil
}

func validSkillHosts(hosts []agenthost.ID) bool {
	if len(hosts) == 0 || len(hosts) > 16 {
		return false
	}
	seen := make(map[agenthost.ID]bool, len(hosts))
	for _, host := range hosts {
		if seen[host] {
			return false
		}
		if !slices.Contains([]agenthost.ID{agenthost.IDVSCode, agenthost.IDOpenCode, agenthost.IDCline, agenthost.IDZed}, host) {
			return false
		}
		seen[host] = true
	}
	return true
}

func normalizeSkillHosts(hosts []agenthost.ID) []agenthost.ID {
	seen := make(map[agenthost.ID]bool, len(hosts))
	result := make([]agenthost.ID, 0, len(hosts))
	for _, host := range hosts {
		if !seen[host] {
			seen[host] = true
			result = append(result, host)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func skillAction(purpose string, descriptor skillDescriptor, previous ComponentInspection, remove bool) Action {
	return Action{
		Kind: ActionSkill, Purpose: purpose,
		Package: &PackageChange{
			Source: descriptor.sourceRoot, Target: descriptor.targetRoot, Version: descriptor.version,
			Kind: ComponentSkill, SourceSHA256: descriptor.sourceFingerprint,
			PreviousSHA256: previous.Fingerprint, Remove: remove,
		},
	}
}

func skillActionMatches(change *PackageChange, descriptor skillDescriptor) error {
	if change == nil || change.Source != descriptor.sourceRoot || change.Target != descriptor.targetRoot ||
		change.Version != descriptor.version || change.Kind != ComponentSkill ||
		change.SourceSHA256 != descriptor.sourceFingerprint || change.PreviousSHA256 == "" {
		return errors.New("skill action does not match the reviewed source and target")
	}
	return nil
}

func skillActions(request Request, inspection Inspection, descriptor skillDescriptor) (actions, verification, rollback []Action, blockedReason string) {
	previous, ok := componentInspection(inspection, ComponentSkill)
	if !ok {
		return nil, nil, nil, "Portable Skill inspection is missing from the bound plan."
	}
	verification = []Action{skillAction("verify_portable_skill", descriptor, previous, false)}
	switch previous.State {
	case StateNameConflict, StateMalformed, StateUnreadable, StateUnavailable:
		return nil, verification, nil, "The documented portable Skill target is not safely Corresync-owned."
	case StateAbsent, StateHealthy, StateDisabled, StateStalePath, StateVersionDrift:
	}
	switch request.Operation {
	case OperationSetup, OperationRepair:
		if previous.State != StateHealthy {
			actions = []Action{skillAction("install_portable_skill", descriptor, previous, false)}
			rollback = []Action{skillAction("remove_portable_skill", descriptor, previous, true)}
		}
	case OperationRemove:
		if previous.State != StateAbsent {
			actions = []Action{skillAction("remove_portable_skill", descriptor, previous, true)}
		}
	}
	return actions, verification, rollback, ""
}
