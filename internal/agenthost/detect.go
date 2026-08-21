package agenthost

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nkiyohara/corresync/internal/panicguard"
)

const (
	defaultDetectionTimeout = 2 * time.Second
	maxSearchPathBytes      = 64 << 10
	maxEvidencePathBytes    = 16 << 10
	maxSearchDirectories    = 256
	maxConcurrentHosts      = 8
)

type DetectionStatus string

const (
	StatusConfirmed          DetectionStatus = "confirmed"
	StatusProbable           DetectionStatus = "probable"
	StatusSelectedMissing    DetectionStatus = "selected_missing"
	StatusNotFound           DetectionStatus = "not_found"
	StatusUnsupportedSurface DetectionStatus = "unsupported_surface"
)

type ConnectionStatus string

const (
	ConnectionNotInspected ConnectionStatus = "not_inspected"
	ConnectionNotDetected  ConnectionStatus = "not_detected"
	ConnectionUnsupported  ConnectionStatus = "unsupported"
)

type CacheStatus string

const (
	CacheFresh     CacheStatus = "fresh"
	CacheRefreshed CacheStatus = "refreshed"
	CacheHit       CacheStatus = "cached"
)

type RuntimeContext struct {
	Kind string `json:"kind"`
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type Evidence struct {
	Kind     EvidenceKind `json:"kind"`
	Source   string       `json:"source"`
	Location string       `json:"location"`
}

type ProbeProblem struct {
	HostID ID           `json:"hostId,omitempty"`
	Kind   EvidenceKind `json:"kind,omitempty"`
	Code   string       `json:"code"`
}

type Detection struct {
	Host             Host             `json:"host"`
	Status           DetectionStatus  `json:"status"`
	ConnectionStatus ConnectionStatus `json:"connectionStatus"`
	Evidence         []Evidence       `json:"evidence"`
}

type Failure struct {
	Code string `json:"code"`
}

type Report struct {
	SchemaVersion int            `json:"schemaVersion"`
	Context       RuntimeContext `json:"context"`
	Cache         CacheStatus    `json:"cache"`
	Hosts         []Detection    `json:"hosts"`
	Problems      []ProbeProblem `json:"problems,omitempty"`
	Failure       *Failure       `json:"failure,omitempty"`
}

type Request struct {
	Refresh  bool
	Selected []ID
}

var ErrDetectionTimeout = errors.New("agent-host detection timed out")
var ErrDetectionCancelled = errors.New("agent-host detection cancelled")

type FileSystem interface {
	Stat(string) (fs.FileInfo, error)
	EvalSymlinks(string) (string, error)
}

type operatingSystemFS struct{}

func (operatingSystemFS) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(name)
}

func (operatingSystemFS) EvalSymlinks(name string) (string, error) {
	return filepath.EvalSymlinks(name)
}

type Options struct {
	GOOS             string
	GOARCH           string
	HomeDir          string
	ConfigDir        string
	WorkingDirectory string
	SearchPath       string
	LookupEnv        func(string) (string, bool)
	Timeout          time.Duration
	FileSystem       FileSystem
}

type Detector struct {
	catalog    Catalog
	goos       string
	goarch     string
	home       string
	config     string
	workingDir string
	searchPath string
	lookupEnv  func(string) (string, bool)
	timeout    time.Duration
	fs         FileSystem
	context    RuntimeContext
	baseIssues []ProbeProblem
	probeSlots chan struct{}

	mu    sync.Mutex
	cache *Report
}

func NewDetector(catalog Catalog, options Options) (*Detector, error) {
	if len(catalog.hosts) == 0 {
		return nil, errors.New("agent-host detector requires a catalog")
	}
	lookupEnv := options.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	goos := options.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := options.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	home := options.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	configDir := options.ConfigDir
	if configDir == "" {
		configDir, _ = os.UserConfigDir()
	}
	workingDirectory := options.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory, _ = os.Getwd()
	}
	searchPath := options.SearchPath
	if searchPath == "" {
		searchPath, _ = lookupEnv("PATH")
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultDetectionTimeout
	}
	filesystem := options.FileSystem
	if filesystem == nil {
		filesystem = operatingSystemFS{}
	}

	kind := "local"
	if value, ok := lookupEnv("WSL_DISTRO_NAME"); ok && strings.TrimSpace(value) != "" {
		kind = "wsl"
	} else if value, ok := lookupEnv("SSH_CONNECTION"); ok && strings.TrimSpace(value) != "" {
		kind = "ssh"
	}

	detector := &Detector{
		catalog: catalog, goos: goos, goarch: goarch,
		home: home, config: configDir, workingDir: workingDirectory,
		searchPath: searchPath, lookupEnv: lookupEnv, timeout: timeout,
		fs: filesystem, context: RuntimeContext{Kind: kind, OS: goos, Arch: goarch},
		probeSlots: make(chan struct{}, maxConcurrentHosts),
	}
	for _, root := range []string{home, configDir, workingDirectory} {
		if root != "" && (!isAbsolute(goos, root) || hasControl(root)) {
			return nil, errors.New("agent-host detector roots must be absolute and control-free")
		}
	}
	if len(searchPath) > maxSearchPathBytes {
		detector.searchPath = ""
		detector.baseIssues = append(detector.baseIssues, ProbeProblem{Code: "search_path_too_large"})
	}
	return detector, nil
}

func (detector *Detector) Detect(ctx context.Context, request Request) (Report, error) {
	if !request.Refresh {
		detector.mu.Lock()
		if detector.cache != nil {
			report := cloneReport(*detector.cache)
			detector.mu.Unlock()
			report.Cache = CacheHit
			applySelection(&report, request.Selected)
			return report, nil
		}
		detector.mu.Unlock()
	}

	detectionContext, cancel := context.WithTimeout(ctx, detector.timeout)
	defer cancel()
	hosts := detector.catalog.Hosts()
	type indexedDetection struct {
		index    int
		result   Detection
		problems []ProbeProblem
	}
	results := make(chan indexedDetection, len(hosts))
	for index, host := range hosts {
		panicguard.Go(detectionContext, panicguard.BoundaryBackgroundWork, func() {
			select {
			case detector.probeSlots <- struct{}{}:
			case <-detectionContext.Done():
				return
			}
			result, problems := detector.detectHost(detectionContext, host)
			<-detector.probeSlots
			select {
			case results <- indexedDetection{index: index, result: result, problems: problems}:
			case <-detectionContext.Done():
			}
		})
	}

	indexed := make([]Detection, len(hosts))
	completed := make([]bool, len(hosts))
	report := Report{
		SchemaVersion: 1,
		Context:       detector.context,
		Cache:         CacheFresh,
		Problems:      slices.Clone(detector.baseIssues),
	}
	if request.Refresh {
		report.Cache = CacheRefreshed
	}
	for received := 0; received < len(hosts); received++ {
		select {
		case item := <-results:
			indexed[item.index] = item.result
			completed[item.index] = true
			report.Problems = append(report.Problems, item.problems...)
		case <-detectionContext.Done():
			for index := range indexed {
				if completed[index] {
					report.Hosts = append(report.Hosts, indexed[index])
				}
			}
			failureErr := ErrDetectionTimeout
			report.Failure = &Failure{Code: "timeout"}
			if errors.Is(ctx.Err(), context.Canceled) {
				failureErr = ErrDetectionCancelled
				report.Failure.Code = "cancelled"
			}
			sortReport(&report)
			applySelection(&report, request.Selected)
			return report, failureErr
		}
	}
	report.Hosts = indexed
	sortReport(&report)
	cached := cloneReport(report)
	cached.Cache = CacheFresh
	detector.mu.Lock()
	detector.cache = &cached
	detector.mu.Unlock()
	applySelection(&report, request.Selected)
	return report, nil
}

func (detector *Detector) detectHost(ctx context.Context, host Host) (Detection, []ProbeProblem) {
	evidence := make([]Evidence, 0, 3)
	problems := make([]ProbeProblem, 0)
	seen := make(map[string]bool)

	if item, itemProblems, ok := detector.findExecutable(ctx, host); ok {
		evidence = append(evidence, item)
		seen[pathKey(detector.goos, item.Location)] = true
		problems = append(problems, itemProblems...)
	} else {
		problems = append(problems, itemProblems...)
	}
	for _, candidate := range host.Detection.paths {
		if ctx.Err() != nil {
			break
		}
		if candidate.platform != "" && candidate.platform != detector.goos {
			continue
		}
		location, ok := detector.resolveKnownPath(candidate)
		if !ok || seen[pathKey(detector.goos, location)] {
			continue
		}
		resolved, exists, err := detector.inspect(location, candidate.kind)
		if err != nil {
			problems = append(problems, ProbeProblem{HostID: host.ID, Kind: candidate.kind, Code: "unreadable"})
			continue
		}
		if !exists || seen[pathKey(detector.goos, resolved)] {
			continue
		}
		seen[pathKey(detector.goos, resolved)] = true
		evidence = append(evidence, Evidence{Kind: candidate.kind, Source: "known_path", Location: resolved})
	}

	status := StatusNotFound
	connection := ConnectionNotDetected
	hasPrimary := false
	for _, item := range evidence {
		if item.Kind == EvidenceExecutable || item.Kind == EvidenceApplication {
			hasPrimary = true
			break
		}
	}
	if hasPrimary {
		status = StatusConfirmed
		connection = ConnectionNotInspected
		if host.Support == SupportCatalogOnly {
			status = StatusUnsupportedSurface
			connection = ConnectionUnsupported
		}
	} else if len(evidence) > 0 {
		status = StatusProbable
		connection = ConnectionNotInspected
		if host.Support == SupportCatalogOnly {
			connection = ConnectionUnsupported
		}
	}
	return Detection{Host: host, Status: status, ConnectionStatus: connection, Evidence: evidence}, problems
}

func (detector *Detector) findExecutable(ctx context.Context, host Host) (Evidence, []ProbeProblem, bool) {
	directories, pathProblem := detector.searchDirectories()
	problems := make([]ProbeProblem, 0)
	if pathProblem != "" {
		problems = append(problems, ProbeProblem{HostID: host.ID, Kind: EvidenceExecutable, Code: pathProblem})
	}
	for _, command := range host.Detection.Commands {
		for _, directory := range directories {
			for _, name := range executableNames(detector.goos, command) {
				if ctx.Err() != nil {
					return Evidence{}, problems, false
				}
				candidate := joinPath(detector.goos, directory, name)
				resolved, exists, err := detector.inspect(candidate, EvidenceExecutable)
				if err != nil {
					problems = append(problems, ProbeProblem{HostID: host.ID, Kind: EvidenceExecutable, Code: "unreadable"})
					continue
				}
				if exists {
					source := "path"
					if !containsDirectory(detector.goos, detector.searchPath, directory, detector.workingDir) {
						source = "common_directory"
					}
					return Evidence{Kind: EvidenceExecutable, Source: source, Location: resolved}, problems, true
				}
			}
		}
	}
	return Evidence{}, problems, false
}

func (detector *Detector) searchDirectories() ([]string, string) {
	directories := make([]string, 0, 32)
	seen := make(map[string]bool)
	problem := ""
	parts := splitSearchPath(detector.goos, detector.searchPath)
	if len(parts) > maxSearchDirectories {
		parts = parts[:maxSearchDirectories]
		problem = "search_path_truncated"
	}
	for _, directory := range parts {
		directory = unquoteSearchDirectory(detector.goos, directory)
		if directory == "" || hasControl(directory) {
			continue
		}
		if !isAbsolute(detector.goos, directory) {
			if detector.workingDir == "" {
				continue
			}
			directory = joinPath(detector.goos, detector.workingDir, directory)
		}
		appendUniquePath(detector.goos, &directories, seen, cleanPath(detector.goos, directory))
	}
	for _, directory := range detector.commonExecutableDirectories() {
		appendUniquePath(detector.goos, &directories, seen, directory)
	}
	return directories, problem
}

func (detector *Detector) commonExecutableDirectories() []string {
	directories := make([]string, 0, 24)
	addHome := func(parts ...string) {
		if detector.home != "" {
			directories = append(directories, joinPath(detector.goos, append([]string{detector.home}, parts...)...))
		}
	}
	addEnv := func(name string, parts ...string) {
		root, ok := detector.lookupEnv(name)
		if ok && isAbsolute(detector.goos, root) && !hasControl(root) {
			directories = append(directories, joinPath(detector.goos, append([]string{root}, parts...)...))
		}
	}

	addHome(".local", "bin")
	addHome("bin")
	addHome(".cargo", "bin")
	addHome("go", "bin")
	addHome(".volta", "bin")
	addHome(".bun", "bin")
	addHome(".asdf", "shims")
	addHome(".local", "share", "mise", "shims")
	addHome(".fnm", "current", "bin")
	addHome(".local", "share", "fnm", "current", "bin")
	addHome(".npm-global", "bin")
	addHome(".yarn", "bin")
	addHome(".local", "share", "pnpm")
	switch detector.goos {
	case "darwin":
		directories = append(directories, "/opt/homebrew/bin", "/usr/local/bin")
		addHome("Library", "pnpm")
	case "windows":
		addEnv("LOCALAPPDATA", "Microsoft", "WindowsApps")
		addEnv("LOCALAPPDATA", "pnpm")
		addEnv("APPDATA", "npm")
	default:
		directories = append(directories, "/usr/local/bin", "/snap/bin")
	}
	return directories
}

func (detector *Detector) resolveKnownPath(candidate knownPath) (string, bool) {
	root := ""
	switch candidate.root {
	case rootHome:
		root = detector.home
	case rootConfig:
		root = detector.config
	case rootApplications:
		if detector.goos == "darwin" {
			root = "/Applications"
		}
	case rootLocalAppData:
		root, _ = detector.lookupEnv("LOCALAPPDATA")
	case rootAppData:
		root, _ = detector.lookupEnv("APPDATA")
	case rootProgramFiles:
		root, _ = detector.lookupEnv("ProgramFiles")
	case rootProgramFilesX86:
		root, _ = detector.lookupEnv("ProgramFiles(x86)")
	}
	if root == "" || !isAbsolute(detector.goos, root) || hasControl(root) {
		return "", false
	}
	return joinPath(detector.goos, append([]string{root}, candidate.parts...)...), true
}

func (detector *Detector) inspect(location string, kind EvidenceKind) (string, bool, error) {
	info, err := detector.fs.Stat(location)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	resolved, err := detector.fs.EvalSymlinks(location)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(resolved) > maxEvidencePathBytes || hasControl(resolved) || !isAbsolute(detector.goos, resolved) {
		return "", false, errors.New("resolved evidence path is unsafe")
	}
	resolved = cleanPath(detector.goos, resolved)
	if pathKey(detector.goos, resolved) != pathKey(detector.goos, location) {
		info, err = detector.fs.Stat(resolved)
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
	}
	switch kind {
	case EvidenceExecutable:
		if !info.Mode().IsRegular() || (detector.goos != "windows" && info.Mode().Perm()&0o111 == 0) {
			return "", false, nil
		}
	case EvidenceApplication:
		if !info.IsDir() && !info.Mode().IsRegular() {
			return "", false, nil
		}
	case EvidenceConfig:
		if !info.Mode().IsRegular() && !info.IsDir() {
			return "", false, nil
		}
	default:
		return "", false, fmt.Errorf("unknown evidence kind %q", kind)
	}
	return resolved, true, nil
}

func applySelection(report *Report, selected []ID) {
	selection := make(map[ID]bool, len(selected))
	for _, id := range selected {
		selection[id] = true
	}
	for index := range report.Hosts {
		item := &report.Hosts[index]
		if item.Status == StatusNotFound && selection[item.Host.ID] {
			item.Status = StatusSelectedMissing
		}
	}
	sortReport(report)
}

func sortReport(report *Report) {
	sort.SliceStable(report.Hosts, func(left, right int) bool {
		leftRank := detectionRank(report.Hosts[left].Status)
		rightRank := detectionRank(report.Hosts[right].Status)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return strings.ToLower(report.Hosts[left].Host.DisplayName) < strings.ToLower(report.Hosts[right].Host.DisplayName)
	})
	sort.Slice(report.Problems, func(left, right int) bool {
		if report.Problems[left].HostID != report.Problems[right].HostID {
			return report.Problems[left].HostID < report.Problems[right].HostID
		}
		if report.Problems[left].Kind != report.Problems[right].Kind {
			return report.Problems[left].Kind < report.Problems[right].Kind
		}
		return report.Problems[left].Code < report.Problems[right].Code
	})
}

func detectionRank(status DetectionStatus) int {
	switch status {
	case StatusConfirmed:
		return 0
	case StatusProbable:
		return 1
	case StatusUnsupportedSurface:
		return 2
	case StatusSelectedMissing:
		return 3
	case StatusNotFound:
		return 4
	default:
		return 5
	}
}

func cloneReport(report Report) Report {
	report.Hosts = slices.Clone(report.Hosts)
	for index := range report.Hosts {
		report.Hosts[index].Host = cloneHost(report.Hosts[index].Host)
		report.Hosts[index].Evidence = slices.Clone(report.Hosts[index].Evidence)
	}
	report.Problems = slices.Clone(report.Problems)
	if report.Failure != nil {
		failure := *report.Failure
		report.Failure = &failure
	}
	return report
}

func executableNames(goos, command string) []string {
	if goos != "windows" || strings.Contains(command, ".") {
		return []string{command}
	}
	return []string{command + ".exe", command + ".cmd", command + ".bat", command}
}

func splitSearchPath(goos, value string) []string {
	separator := ":"
	if goos == "windows" {
		separator = ";"
	}
	return strings.Split(value, separator)
}

func containsDirectory(goos, searchPath, directory, workingDirectory string) bool {
	for _, candidate := range splitSearchPath(goos, searchPath) {
		candidate = unquoteSearchDirectory(goos, candidate)
		if candidate == "" || hasControl(candidate) {
			continue
		}
		if !isAbsolute(goos, candidate) {
			candidate = joinPath(goos, workingDirectory, candidate)
		}
		if pathKey(goos, candidate) == pathKey(goos, directory) {
			return true
		}
	}
	return false
}

func unquoteSearchDirectory(goos, value string) string {
	if goos == "windows" && len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return value[1 : len(value)-1]
	}
	return value
}

func appendUniquePath(goos string, target *[]string, seen map[string]bool, value string) {
	key := pathKey(goos, value)
	if value == "" || seen[key] {
		return
	}
	seen[key] = true
	*target = append(*target, value)
}

func isAbsolute(goos, value string) bool {
	if goos != "windows" {
		return strings.HasPrefix(value, "/")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	return strings.HasPrefix(value, "//") ||
		(len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && value[2] == '/')
}

func joinPath(goos string, parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	if goos == "windows" {
		converted := make([]string, len(parts))
		for index, part := range parts {
			converted[index] = strings.ReplaceAll(part, "\\", "/")
		}
		unc := strings.HasPrefix(converted[0], "//")
		joined := path.Join(converted...)
		if unc && !strings.HasPrefix(joined, "//") {
			joined = "/" + joined
		}
		return strings.ReplaceAll(joined, "/", "\\")
	}
	return path.Join(parts...)
}

func cleanPath(goos, value string) string {
	return joinPath(goos, value)
}

func pathKey(goos, value string) string {
	value = cleanPath(goos, value)
	if goos == "windows" {
		return strings.ToLower(value)
	}
	return value
}
