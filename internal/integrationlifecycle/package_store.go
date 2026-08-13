package integrationlifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/filelock"
)

const (
	managedMarketplaceName = "corresync-local"
	packageMarkerName      = ".corresync-package.json"
	maximumPackageFileSize = 256 << 10
)

var packageVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

type packageDescriptor struct {
	host              agenthost.ID
	kind              Component
	sourceRoot        string
	configurationRoot string
	managedRoot       string
	targetRoot        string
	installSource     string
	version           string
	sourceFingerprint string
	files             map[string][]byte
}

type packageMarker struct {
	SchemaVersion int          `json:"schemaVersion"`
	Host          agenthost.ID `json:"host"`
	Version       string       `json:"version"`
	SourceSHA256  string       `json:"sourceSha256"`
}

type PackageStore struct{}

func (PackageStore) Describe(environment Environment, host agenthost.ID) (packageDescriptor, bool, error) {
	kind, supported := nativePackageKind(host)
	if !supported {
		return packageDescriptor{}, false, nil
	}
	if environment.BundleDirectory == "" || environment.ManagedDirectory == "" {
		return packageDescriptor{}, false, nil
	}
	descriptor := packageDescriptor{
		host: host, kind: kind, sourceRoot: environment.BundleDirectory,
		configurationRoot: environment.ConfigDirectory, managedRoot: environment.ManagedDirectory,
		targetRoot: filepath.Join(environment.ManagedDirectory, string(host)),
	}
	for _, root := range []string{environment.BundleDirectory, environment.ManagedDirectory} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return descriptor, true, errors.New("integration bundle and managed package directories must be clean and absolute")
		}
	}
	if err := validateBundleRoot(environment.BundleDirectory); err != nil {
		return descriptor, true, err
	}
	var sourceFiles []string
	//nolint:exhaustive // nativePackageKind admits only the four native-package hosts below.
	switch host {
	case agenthost.IDCodex:
		sourceFiles = append(commonPluginFiles(),
			".agents/plugins/marketplace.json",
			"plugins/corresync/.codex-plugin/plugin.json",
		)
		descriptor.installSource = descriptor.targetRoot
	case agenthost.IDClaudeCode:
		sourceFiles = append(commonPluginFiles(),
			".claude-plugin/marketplace.json",
			"plugins/corresync/.claude-plugin/plugin.json",
		)
		descriptor.installSource = descriptor.targetRoot
	case agenthost.IDGitHubCopilot:
		sourceFiles = append(commonPluginFiles(), "plugins/corresync/.claude-plugin/plugin.json")
		descriptor.installSource = filepath.Join(descriptor.targetRoot, "plugins", "corresync")
	case agenthost.IDGeminiCLI:
		sourceFiles = []string{
			"integrations/gemini-cli/corresync/gemini-extension.json",
			"integrations/gemini-cli/corresync/skills/corresync/SKILL.md",
		}
		descriptor.installSource = descriptor.targetRoot
	}
	descriptor.files = make(map[string][]byte, len(sourceFiles)+1)
	hash := sha256.New()
	for _, relative := range sourceFiles {
		data, err := readPackageSource(environment.BundleDirectory, relative)
		if err != nil {
			return descriptor, true, fmt.Errorf("read integration package source %s: %w", relative, err)
		}
		stagedRelative := relative
		if host == agenthost.IDGeminiCLI {
			stagedRelative = strings.TrimPrefix(relative, "integrations/gemini-cli/corresync/")
		}
		if strings.HasSuffix(relative, "plugin.json") || strings.HasSuffix(relative, "marketplace.json") || strings.HasSuffix(relative, "gemini-extension.json") {
			data, err = preparePackageJSON(relative, data)
			if err != nil {
				return descriptor, true, err
			}
		}
		descriptor.files[stagedRelative] = data
		_, _ = io.WriteString(hash, stagedRelative)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	descriptor.version, _ = packageVersion(descriptor.files)
	if !packageVersionPattern.MatchString(descriptor.version) {
		return descriptor, true, fmt.Errorf("integration package version %q is not SemVer", descriptor.version)
	}
	descriptor.sourceFingerprint = hex.EncodeToString(hash.Sum(nil))
	return descriptor, true, nil
}

func validateBundleRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("integration bundle root must be clean and absolute")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!OwnedByCurrentUserOrRoot(info) || WritableByOtherUsers(info) {
		return errors.Join(err, errors.New("integration bundle root has an unsafe type, owner, or mode"))
	}
	return nil
}

func nativePackageKind(host agenthost.ID) (Component, bool) {
	switch host {
	case agenthost.IDCodex, agenthost.IDClaudeCode, agenthost.IDGitHubCopilot:
		return ComponentPlugin, true
	case agenthost.IDGeminiCLI:
		return ComponentExtension, true
	case agenthost.IDClaudeDesktop, agenthost.IDKiro, agenthost.IDQwenCode, agenthost.IDQoder,
		agenthost.IDKimiCode, agenthost.IDVSCode,
		agenthost.IDCursor, agenthost.IDWindsurf, agenthost.IDOpenCode, agenthost.IDCline,
		agenthost.IDRooCode, agenthost.IDZed, agenthost.IDGoose:
		return "", false
	}
	return "", false
}

func commonPluginFiles() []string {
	return []string{
		"plugins/corresync/assets/icon.svg",
		"plugins/corresync/skills/corresync/SKILL.md",
		"plugins/corresync/skills/corresync/agents/openai.yaml",
	}
}

func readPackageSource(root, relative string) ([]byte, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	if candidate, err := filepath.Rel(root, path); err != nil || candidate == ".." || filepath.IsAbs(candidate) ||
		strings.HasPrefix(candidate, ".."+string(filepath.Separator)) {
		return nil, errors.New("integration package source escapes its reviewed root")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!OwnedByCurrentUserOrRoot(info) || WritableByOtherUsers(info) ||
		info.Size() > maximumPackageFileSize {
		return nil, errors.New("integration package source is not a bounded regular file")
	}
	for directory := filepath.Dir(path); directory != root; directory = filepath.Dir(directory) {
		parent, statErr := os.Lstat(directory)
		if statErr != nil {
			return nil, statErr
		}
		if !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 ||
			!OwnedByCurrentUserOrRoot(parent) || WritableByOtherUsers(parent) {
			return nil, errors.New("integration package source has an unsafe parent directory")
		}
	}
	// #nosec G304 -- relative is selected from a fixed package allowlist.
	data, err := readOpenedBounded(path, info, maximumPackageFileSize)
	if err != nil || len(data) > maximumPackageFileSize {
		return nil, errors.New("read bounded integration package source")
	}
	return data, nil
}

func preparePackageJSON(relative string, data []byte) ([]byte, error) {
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&document); err != nil || document == nil {
		return nil, fmt.Errorf("decode integration package manifest %s", relative)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode integration package manifest %s: trailing JSON value", relative)
	}
	delete(document, "mcpServers")
	if strings.HasSuffix(relative, "marketplace.json") {
		document["name"] = managedMarketplaceName
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode integration package manifest %s: %w", relative, err)
	}
	return append(encoded, '\n'), nil
}

func packageVersion(files map[string][]byte) (string, bool) {
	keys := make([]string, 0, len(files))
	for name := range files {
		if strings.HasSuffix(name, "plugin.json") || strings.HasSuffix(name, "gemini-extension.json") {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	for _, name := range keys {
		var document struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(files[name], &document) == nil && document.Version != "" {
			return document.Version, true
		}
	}
	return "", false
}

func (PackageStore) InspectTarget(descriptor packageDescriptor) (State, string, string, error) {
	info, err := os.Lstat(descriptor.targetRoot)
	if errors.Is(err, os.ErrNotExist) {
		return StateAbsent, "", absentFileFingerprint(descriptor.targetRoot), nil
	}
	if err != nil {
		return StateUnreadable, "", "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || WritableByOtherUsers(info) {
		return StateNameConflict, "", "", errors.New("managed integration package target is not Corresync-owned")
	}
	data, err := readPackageMarker(descriptor.targetRoot)
	if err != nil {
		return StateNameConflict, "", "", err
	}
	var marker packageMarker
	if err := json.Unmarshal(data, &marker); err != nil || marker.SchemaVersion != SchemaVersion || marker.Host != descriptor.host {
		return StateNameConflict, "", "", errors.New("managed integration package marker is invalid")
	}
	fingerprint, current, err := packageTreeFingerprint(descriptor, data)
	if err != nil {
		return StateNameConflict, marker.Version, "", err
	}
	if marker.Version != descriptor.version || marker.SourceSHA256 != descriptor.sourceFingerprint || !current {
		return StateVersionDrift, marker.Version, fingerprint, nil
	}
	return StateHealthy, marker.Version, fingerprint, nil
}

func (PackageStore) Remove(ctx context.Context, descriptor packageDescriptor, expectedFingerprint string) (returnErr error) {
	if err := validateManagedPackageTarget(descriptor); err != nil {
		return err
	}
	lock, err := filelock.Acquire(ctx, descriptor.targetRoot+".corresync.lock")
	if err != nil {
		return fmt.Errorf("acquire managed package lock: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	if err := validateManagedPackageTarget(descriptor); err != nil {
		return err
	}
	state, _, fingerprint, err := (PackageStore{}).InspectTarget(descriptor)
	if err != nil {
		return err
	}
	if fingerprint != expectedFingerprint {
		return errors.New("managed package target changed after preview")
	}
	if state == StateAbsent {
		return nil
	}
	if err := requireManagedPackage(descriptor, descriptor.targetRoot); err != nil {
		return fmt.Errorf("refuse to remove unmanaged package directory: %w", err)
	}
	backup := descriptor.targetRoot + ".corresync.bak"
	if _, err := os.Lstat(backup); err == nil {
		if err := requireManagedPackage(descriptor, backup); err != nil {
			return fmt.Errorf("refuse unsafe managed package recovery directory: %w", err)
		}
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(descriptor.targetRoot, backup); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(descriptor.targetRoot))
}

func (PackageStore) Stage(ctx context.Context, descriptor packageDescriptor, expectedSourceFingerprint, expectedTargetFingerprint string) (returnErr error) {
	if descriptor.sourceFingerprint != expectedSourceFingerprint {
		return errors.New("integration package source changed after preview")
	}
	parent := filepath.Dir(descriptor.targetRoot)
	if err := validateManagedPackageTarget(descriptor); err != nil {
		return err
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if err := validateManagedPackageTarget(descriptor); err != nil {
		return err
	}
	lock, err := filelock.Acquire(ctx, descriptor.targetRoot+".corresync.lock")
	if err != nil {
		return fmt.Errorf("acquire managed package lock: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	if err := validateManagedPackageTarget(descriptor); err != nil {
		return err
	}
	_, _, targetFingerprint, err := (PackageStore{}).InspectTarget(descriptor)
	if err != nil {
		return err
	}
	if targetFingerprint != expectedTargetFingerprint {
		return errors.New("managed package target changed after preview")
	}

	temporary, err := os.MkdirTemp(parent, ".corresync-package-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	keys := make([]string, 0, len(descriptor.files))
	for relative := range descriptor.files {
		keys = append(keys, relative)
	}
	sort.Strings(keys)
	for _, relative := range keys {
		if err := writePackageFile(temporary, relative, descriptor.files[relative]); err != nil {
			return err
		}
	}
	marker, err := json.MarshalIndent(packageMarker{
		SchemaVersion: SchemaVersion, Host: descriptor.host, Version: descriptor.version,
		SourceSHA256: descriptor.sourceFingerprint,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := writePackageFile(temporary, packageMarkerName, append(marker, '\n')); err != nil {
		return err
	}
	if err := syncPackageTree(temporary); err != nil {
		return err
	}
	backup := descriptor.targetRoot + ".corresync.bak"
	if _, err := os.Lstat(backup); err == nil {
		if err := requireManagedPackage(descriptor, backup); err != nil {
			return fmt.Errorf("refuse unsafe managed package recovery directory: %w", err)
		}
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	previous := false
	if _, err := os.Lstat(descriptor.targetRoot); err == nil {
		if err := requireManagedPackage(descriptor, descriptor.targetRoot); err != nil {
			return fmt.Errorf("refuse to replace unmanaged package directory: %w", err)
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

func writePackageFile(root, relative string, data []byte) error {
	path := filepath.Join(root, filepath.FromSlash(relative))
	candidate, err := filepath.Rel(root, path)
	if err != nil || candidate == ".." || filepath.IsAbs(candidate) || strings.HasPrefix(candidate, ".."+string(filepath.Separator)) {
		return errors.New("managed package file escapes its root")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// #nosec G304 -- path is constrained to the private staging root above.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	return errors.Join(file.Sync(), file.Close())
}

func syncPackageTree(root string) error {
	// writePackageFile syncs every staged file on its writable handle. This
	// pass anchors the finished tree, rejects swaps, and syncs directories.
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	var directories []string
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			directories = append(directories, path)
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pathInfo, err := entry.Info()
		if err != nil {
			return err
		}
		anchoredInfo, err := rootHandle.Lstat(relative)
		if err != nil || !os.SameFile(pathInfo, anchoredInfo) || anchoredInfo.Mode()&os.ModeSymlink != 0 {
			return errors.Join(err, errors.New("managed package staging changed while syncing"))
		}
		if anchoredInfo.IsDir() {
			directories = append(directories, path)
			return nil
		}
		if !anchoredInfo.Mode().IsRegular() {
			return errors.Join(err, errors.New("managed package staging contains an unsafe file"))
		}
		return nil
	})
	if err := errors.Join(walkErr, rootHandle.Close()); err != nil {
		return err
	}
	slices.Reverse(directories)
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func requireManagedPackage(descriptor packageDescriptor, root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || WritableByOtherUsers(info) {
		return errors.New("package target is not a current-user directory")
	}
	data, err := readPackageMarker(root)
	if err != nil {
		return err
	}
	var marker packageMarker
	if err := json.Unmarshal(data, &marker); err != nil || marker.SchemaVersion != SchemaVersion || marker.Host != descriptor.host {
		return errors.New("package target lacks a valid Corresync ownership marker")
	}
	reviewed := descriptor
	reviewed.targetRoot = root
	_, _, err = packageTreeFingerprint(reviewed, data)
	return err
}

func packageTreeFingerprint(descriptor packageDescriptor, marker []byte) (string, bool, error) {
	expected := make(map[string][]byte, len(descriptor.files)+1)
	expectedDirectories := map[string]bool{".": true}
	for relative, data := range descriptor.files {
		relative = filepath.ToSlash(relative)
		expected[relative] = data
		for directory := filepath.ToSlash(filepath.Dir(relative)); directory != "."; directory = filepath.ToSlash(filepath.Dir(directory)) {
			expectedDirectories[directory] = true
		}
	}
	expected[packageMarkerName] = marker
	actual := make(map[string][]byte, len(expected))
	err := filepath.WalkDir(descriptor.targetRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == descriptor.targetRoot {
			return nil
		}
		relative, err := filepath.Rel(descriptor.targetRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			info, statErr := entry.Info()
			if statErr != nil || !expectedDirectories[relative] || info.Mode()&os.ModeSymlink != 0 ||
				!ownedByCurrentUser(info) || WritableByOtherUsers(info) {
				return errors.New("managed package tree contains an unsafe or unexpected directory")
			}
			return nil
		}
		if len(actual) >= len(expected) {
			return errors.New("managed package tree contains unexpected files")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) ||
			WritableByOtherUsers(info) || info.Size() > maximumPackageFileSize {
			return errors.New("managed package tree contains an unsafe file")
		}
		if _, ok := expected[relative]; !ok {
			return errors.New("managed package tree contains an unexpected file")
		}
		data, err := readOpenedBounded(path, info, maximumPackageFileSize)
		if err != nil {
			return err
		}
		actual[relative] = data
		return nil
	})
	if err != nil {
		return "", false, err
	}
	keys := make([]string, 0, len(actual))
	for name := range actual {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	hash := sha256.New()
	current := len(actual) == len(expected)
	for _, name := range keys {
		_, _ = io.WriteString(hash, name)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(actual[name])
		_, _ = hash.Write([]byte{0})
		if name != packageMarkerName && !slices.Equal(actual[name], expected[name]) {
			current = false
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), current, nil
}

func readOpenedBounded(path string, expected os.FileInfo, limit int64) ([]byte, error) {
	// #nosec G304 -- callers select exact files below reviewed roots.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		return nil, errors.New("reviewed file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("reviewed file exceeds its parser bound")
	}
	return data, nil
}

func validateManagedPackageTarget(descriptor packageDescriptor) error {
	if descriptor.configurationRoot == "" || descriptor.managedRoot == "" ||
		!filepath.IsAbs(descriptor.configurationRoot) || !filepath.IsAbs(descriptor.managedRoot) {
		return errors.New("managed integration package roots must be absolute")
	}
	for _, target := range []string{descriptor.managedRoot, descriptor.targetRoot} {
		relative, err := filepath.Rel(descriptor.configurationRoot, target)
		if err != nil || relative == ".." || filepath.IsAbs(relative) ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("managed integration package target escapes the configuration root")
		}
	}
	if info, err := os.Lstat(descriptor.configurationRoot); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || WritableByOtherUsers(info) {
			return errors.New("managed integration package configuration root has an unsafe type, owner, or mode")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for directory := filepath.Dir(descriptor.targetRoot); directory != descriptor.configurationRoot; directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) || WritableByOtherUsers(info) {
			return fmt.Errorf("managed integration package parent has an unsafe type, owner, or mode: %s", directory)
		}
	}
	return nil
}

func readPackageMarker(root string) ([]byte, error) {
	path := filepath.Join(root, packageMarkerName)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) ||
		WritableByOtherUsers(info) || info.Size() > maximumPackageFileSize {
		return nil, errors.New("managed integration package marker is unsafe")
	}
	// #nosec G304 -- path is the fixed marker below a reviewed managed root.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("managed integration package marker changed while opening")
	}
	return io.ReadAll(io.LimitReader(file, maximumPackageFileSize+1))
}
