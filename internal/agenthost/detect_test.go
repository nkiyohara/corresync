package agenthost

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type syntheticFileInfo struct {
	name string
	mode fs.FileMode
}

func (info syntheticFileInfo) Name() string       { return info.name }
func (info syntheticFileInfo) Size() int64        { return 0 }
func (info syntheticFileInfo) Mode() fs.FileMode  { return info.mode }
func (info syntheticFileInfo) ModTime() time.Time { return time.Time{} }
func (info syntheticFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info syntheticFileInfo) Sys() any           { return nil }

type syntheticFS struct {
	mu       sync.RWMutex
	entries  map[string]fs.FileInfo
	resolved map[string]string
	delay    time.Duration
	active   int
	maximum  int
}

func newSyntheticFS() *syntheticFS {
	return &syntheticFS{entries: make(map[string]fs.FileInfo), resolved: make(map[string]string)}
}

func (filesystem *syntheticFS) addFile(name string, executable bool) {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	mode := fs.FileMode(0o600)
	if executable {
		mode = 0o700
	}
	filesystem.entries[name] = syntheticFileInfo{name: pathBase(name), mode: mode}
}

func (filesystem *syntheticFS) addDirectory(name string) {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	filesystem.entries[name] = syntheticFileInfo{name: pathBase(name), mode: fs.ModeDir | 0o700}
}

func (filesystem *syntheticFS) addSymlink(name, target string) {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	filesystem.entries[name] = syntheticFileInfo{name: pathBase(name), mode: 0o700}
	filesystem.resolved[name] = target
}

func (filesystem *syntheticFS) Stat(name string) (fs.FileInfo, error) {
	filesystem.mu.Lock()
	delay := filesystem.delay
	filesystem.active++
	filesystem.maximum = max(filesystem.maximum, filesystem.active)
	filesystem.mu.Unlock()
	defer func() {
		filesystem.mu.Lock()
		filesystem.active--
		filesystem.mu.Unlock()
	}()
	if delay > 0 {
		time.Sleep(delay)
	}
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	info, ok := filesystem.entries[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return info, nil
}

func (filesystem *syntheticFS) setDelay(delay time.Duration) {
	filesystem.mu.Lock()
	defer filesystem.mu.Unlock()
	filesystem.delay = delay
}

func (filesystem *syntheticFS) maximumConcurrency() int {
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	return filesystem.maximum
}

func (filesystem *syntheticFS) EvalSymlinks(name string) (string, error) {
	filesystem.mu.RLock()
	defer filesystem.mu.RUnlock()
	if target, ok := filesystem.resolved[name]; ok {
		return target, nil
	}
	if _, ok := filesystem.entries[name]; !ok {
		return "", fs.ErrNotExist
	}
	return name, nil
}

func TestDetectorFindsMultipleHostsWithoutReadingOrExecuting(t *testing.T) {
	t.Parallel()

	filesystem := newSyntheticFS()
	filesystem.addFile("/opt/tools/codex", true)
	filesystem.addSymlink("/opt/tools/claude", "/managed/claude")
	filesystem.addFile("/managed/claude", true)
	filesystem.addDirectory("/home/person/.kimi")

	catalog := testCatalog(t,
		testHost("codex", SupportVerified, []string{"codex"}, nil),
		testHost("claude", SupportVerified, []string{"claude"}, nil),
		testHost("kimi", SupportConfigOnly, []string{"kimi"}, []knownPath{known("", rootHome, EvidenceConfig, ".kimi")}),
	)
	detector := testDetector(t, catalog, Options{
		GOOS: "linux", GOARCH: "amd64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
		WorkingDirectory: "/work", SearchPath: "/opt/tools", FileSystem: filesystem,
	})
	report, err := detector.Detect(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	assertDetection(t, report, "codex", StatusConfirmed, Evidence{Kind: EvidenceExecutable, Source: "path", Location: "/opt/tools/codex"})
	assertDetection(t, report, "claude", StatusConfirmed, Evidence{Kind: EvidenceExecutable, Source: "path", Location: "/managed/claude"})
	assertDetection(t, report, "kimi", StatusProbable, Evidence{Kind: EvidenceConfig, Source: "known_path", Location: "/home/person/.kimi"})
	for _, item := range report.Hosts {
		if item.ConnectionStatus != ConnectionNotInspected {
			t.Errorf("%s connection status = %q", item.Host.ID, item.ConnectionStatus)
		}
	}
}

func TestDetectorCoversCommonManagersApplicationsWindowsAndSymlinks(t *testing.T) {
	t.Parallel()

	t.Run("mise shim", func(t *testing.T) {
		filesystem := newSyntheticFS()
		filesystem.addFile("/home/person/.local/share/mise/shims/codex", true)
		detector := testDetector(t, testCatalog(t, testHost("codex", SupportVerified, []string{"codex"}, nil)), Options{
			GOOS: "linux", GOARCH: "arm64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
			WorkingDirectory: "/work", SearchPath: "/usr/bin", FileSystem: filesystem,
		})
		report, err := detector.Detect(t.Context(), Request{})
		if err != nil {
			t.Fatal(err)
		}
		assertDetection(t, report, "codex", StatusConfirmed, Evidence{
			Kind: EvidenceExecutable, Source: "common_directory", Location: "/home/person/.local/share/mise/shims/codex",
		})
	})

	t.Run("macOS app", func(t *testing.T) {
		filesystem := newSyntheticFS()
		filesystem.addDirectory("/Applications/Cursor.app")
		host := testHost("cursor", SupportExperimental, nil, []knownPath{known("darwin", rootApplications, EvidenceApplication, "Cursor.app")})
		detector := testDetector(t, testCatalog(t, host), Options{
			GOOS: "darwin", GOARCH: "arm64", HomeDir: "/Users/person", ConfigDir: "/Users/person/Library/Application Support",
			WorkingDirectory: "/work", SearchPath: "/usr/bin", FileSystem: filesystem,
		})
		report, err := detector.Detect(t.Context(), Request{})
		if err != nil {
			t.Fatal(err)
		}
		assertDetection(t, report, "cursor", StatusConfirmed, Evidence{Kind: EvidenceApplication, Source: "known_path", Location: "/Applications/Cursor.app"})
	})

	t.Run("Windows manager and application", func(t *testing.T) {
		filesystem := newSyntheticFS()
		filesystem.addFile(`C:\Users\person\AppData\Local\Microsoft\WindowsApps\codex.exe`, false)
		filesystem.addFile(`C:\Users\person\AppData\Local\Programs\Microsoft VS Code\Code.exe`, false)
		catalog := testCatalog(t,
			testHost("codex", SupportVerified, []string{"codex"}, nil),
			testHost("vscode", SupportExperimental, []string{"code"}, []knownPath{
				known("windows", rootLocalAppData, EvidenceApplication, "Programs", "Microsoft VS Code", "Code.exe"),
			}),
		)
		detector := testDetector(t, catalog, Options{
			GOOS: "windows", GOARCH: "amd64", HomeDir: `C:\Users\person`, ConfigDir: `C:\Users\person\AppData\Roaming`,
			WorkingDirectory: `C:\work`, SearchPath: `C:\Windows\System32`, FileSystem: filesystem,
			LookupEnv: mapLookup(map[string]string{"LOCALAPPDATA": `C:\Users\person\AppData\Local`}),
		})
		report, err := detector.Detect(t.Context(), Request{})
		if err != nil {
			t.Fatal(err)
		}
		assertDetection(t, report, "codex", StatusConfirmed, Evidence{
			Kind: EvidenceExecutable, Source: "common_directory", Location: `C:\Users\person\AppData\Local\Microsoft\WindowsApps\codex.exe`,
		})
		assertDetection(t, report, "vscode", StatusConfirmed, Evidence{
			Kind: EvidenceApplication, Source: "known_path", Location: `C:\Users\person\AppData\Local\Programs\Microsoft VS Code\Code.exe`,
		})
	})
}

func TestDetectorCacheRefreshSelectionAndContextAreExplicit(t *testing.T) {
	t.Parallel()

	filesystem := newSyntheticFS()
	catalog := testCatalog(t, testHost("codex", SupportVerified, []string{"codex"}, nil))
	detector := testDetector(t, catalog, Options{
		GOOS: "linux", GOARCH: "amd64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
		WorkingDirectory: "/work", SearchPath: "/tools", FileSystem: filesystem,
		LookupEnv: mapLookup(map[string]string{"SSH_CONNECTION": "synthetic"}),
	})
	first, err := detector.Detect(t.Context(), Request{Selected: []ID{"codex"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Cache != CacheFresh || first.Context.Kind != "ssh" || first.Hosts[0].Status != StatusSelectedMissing {
		t.Fatalf("first report = %+v", first)
	}

	filesystem.addFile("/tools/codex", true)
	cached, err := detector.Detect(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if cached.Cache != CacheHit || cached.Hosts[0].Status != StatusNotFound {
		t.Fatalf("cached report = %+v", cached)
	}
	refreshed, err := detector.Detect(t.Context(), Request{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Cache != CacheRefreshed || refreshed.Hosts[0].Status != StatusConfirmed {
		t.Fatalf("refreshed report = %+v", refreshed)
	}
}

func TestDetectorReportsTimeoutWithoutCachingPartialResults(t *testing.T) {
	t.Parallel()

	filesystem := newSyntheticFS()
	filesystem.setDelay(25 * time.Millisecond)
	detector := testDetector(t, testCatalog(t, testHost("codex", SupportVerified, []string{"codex"}, nil)), Options{
		GOOS: "linux", GOARCH: "amd64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
		WorkingDirectory: "/work", SearchPath: "/tools", FileSystem: filesystem, Timeout: time.Millisecond,
	})
	report, err := detector.Detect(t.Context(), Request{})
	if !errors.Is(err, ErrDetectionTimeout) || report.Failure == nil || report.Failure.Code != "timeout" {
		t.Fatalf("Detect() = %+v, %v", report, err)
	}
	filesystem.setDelay(0)
	filesystem.addFile("/tools/codex", true)
	retry, err := detector.Detect(t.Context(), Request{})
	if err != nil || retry.Cache != CacheFresh || retry.Hosts[0].Status != StatusConfirmed {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
}

func TestDetectorBoundsFilesystemWorkAcrossRepeatedTimeouts(t *testing.T) {
	t.Parallel()

	filesystem := newSyntheticFS()
	filesystem.setDelay(100 * time.Millisecond)
	detector := testDetector(t, testCatalog(t,
		testHost("host-1", SupportVerified, []string{"host1"}, nil),
		testHost("host-2", SupportVerified, []string{"host2"}, nil),
		testHost("host-3", SupportVerified, []string{"host3"}, nil),
		testHost("host-4", SupportVerified, []string{"host4"}, nil),
		testHost("host-5", SupportVerified, []string{"host5"}, nil),
		testHost("host-6", SupportVerified, []string{"host6"}, nil),
		testHost("host-7", SupportVerified, []string{"host7"}, nil),
		testHost("host-8", SupportVerified, []string{"host8"}, nil),
		testHost("host-9", SupportVerified, []string{"host9"}, nil),
		testHost("host-10", SupportVerified, []string{"host10"}, nil),
	), Options{
		GOOS: "linux", GOARCH: "amd64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
		WorkingDirectory: "/work", SearchPath: "/tools", FileSystem: filesystem, Timeout: time.Millisecond,
	})
	for range 3 {
		if _, err := detector.Detect(t.Context(), Request{Refresh: true}); !errors.Is(err, ErrDetectionTimeout) {
			t.Fatalf("Detect() error = %v, want timeout", err)
		}
	}
	if got := filesystem.maximumConcurrency(); got > maxConcurrentHosts {
		t.Fatalf("filesystem concurrency = %d, want at most %d", got, maxConcurrentHosts)
	}
}

func TestDetectorDistinguishesCancellationFromTimeout(t *testing.T) {
	t.Parallel()

	filesystem := newSyntheticFS()
	filesystem.setDelay(25 * time.Millisecond)
	detector := testDetector(t, testCatalog(t, testHost("codex", SupportVerified, []string{"codex"}, nil)), Options{
		GOOS: "linux", GOARCH: "amd64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
		WorkingDirectory: "/work", SearchPath: "/tools", FileSystem: filesystem, Timeout: time.Second,
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := detector.Detect(ctx, Request{})
	if !errors.Is(err, ErrDetectionCancelled) || report.Failure == nil || report.Failure.Code != "cancelled" {
		t.Fatalf("Detect() = %+v, %v", report, err)
	}
}

func TestDetectorTreatsParentDeadlineAsTimeout(t *testing.T) {
	t.Parallel()

	filesystem := newSyntheticFS()
	filesystem.setDelay(25 * time.Millisecond)
	detector := testDetector(t, testCatalog(t, testHost("codex", SupportVerified, []string{"codex"}, nil)), Options{
		GOOS: "linux", GOARCH: "amd64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
		WorkingDirectory: "/work", SearchPath: "/tools", FileSystem: filesystem, Timeout: time.Second,
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	report, err := detector.Detect(ctx, Request{})
	if !errors.Is(err, ErrDetectionTimeout) || report.Failure == nil || report.Failure.Code != "timeout" {
		t.Fatalf("Detect() = %+v, %v", report, err)
	}
}

func TestDetectorBoundsUntrustedSearchPath(t *testing.T) {
	t.Parallel()

	filesystem := newSyntheticFS()
	catalog := testCatalog(t, testHost("codex", SupportVerified, []string{"codex"}, nil))
	detector := testDetector(t, catalog, Options{
		GOOS: "linux", GOARCH: "amd64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
		WorkingDirectory: "/work", SearchPath: strings.Repeat("/a:", (maxSearchPathBytes/3)+1), FileSystem: filesystem,
	})
	report, err := detector.Detect(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(report.Problems, func(problem ProbeProblem) bool { return problem.Code == "search_path_too_large" }) {
		t.Fatalf("problems = %+v", report.Problems)
	}
}

func TestCatalogOnlyExecutableIsNotReportedAsSupported(t *testing.T) {
	t.Parallel()

	filesystem := newSyntheticFS()
	filesystem.addFile("/tools/aider", true)
	detector := testDetector(t, testCatalog(t, testHost("aider", SupportCatalogOnly, []string{"aider"}, nil)), Options{
		GOOS: "linux", GOARCH: "amd64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
		WorkingDirectory: "/work", SearchPath: "/tools", FileSystem: filesystem,
	})
	report, err := detector.Detect(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Hosts[0].Status != StatusUnsupportedSurface || report.Hosts[0].ConnectionStatus != ConnectionUnsupported {
		t.Fatalf("catalog-only detection = %+v", report.Hosts[0])
	}
}

func TestDefaultCatalogAvoidsKnownAmbiguousExecutableNames(t *testing.T) {
	t.Parallel()

	for _, host := range DefaultCatalog().Hosts() {
		for _, command := range host.Detection.Commands {
			if command == "agent" || command == "cn" || (host.ID == "zed" && command == "zed") {
				t.Errorf("host %q retains ambiguous executable probe %q", host.ID, command)
			}
		}
	}
}

func TestPortablePathHandlingPreservesWindowsUNCAndDeduplicatesCase(t *testing.T) {
	t.Parallel()

	if got := joinPath("windows", `\\server\share`, "Agents", "agent.exe"); got != `\\server\share\Agents\agent.exe` {
		t.Fatalf("joinPath(UNC) = %q", got)
	}
	detector := testDetector(t, testCatalog(t, testHost("codex", SupportVerified, []string{"codex"}, nil)), Options{
		GOOS: "windows", GOARCH: "amd64", HomeDir: `C:\Users\person`, ConfigDir: `C:\Users\person\AppData\Roaming`,
		WorkingDirectory: `C:\work`, SearchPath: `"C:\TOOLS";c:\tools`, FileSystem: newSyntheticFS(),
	})
	directories, _ := detector.searchDirectories()
	count := 0
	for _, directory := range directories {
		if pathKey("windows", directory) == `c:\tools` {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("case-insensitive PATH count = %d in %#v", count, directories)
	}
}

func TestDetectorRejectsControlCharactersFromSymlinkResolution(t *testing.T) {
	t.Parallel()

	filesystem := newSyntheticFS()
	filesystem.addSymlink("/tools/codex", "/managed/codex\nspoof")
	detector := testDetector(t, testCatalog(t, testHost("codex", SupportVerified, []string{"codex"}, nil)), Options{
		GOOS: "linux", GOARCH: "amd64", HomeDir: "/home/person", ConfigDir: "/home/person/.config",
		WorkingDirectory: "/work", SearchPath: "/tools", FileSystem: filesystem,
	})
	report, err := detector.Detect(t.Context(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Hosts[0].Status != StatusNotFound ||
		!slices.ContainsFunc(report.Problems, func(problem ProbeProblem) bool { return problem.Code == "unreadable" }) {
		t.Fatalf("unsafe symlink report = %+v", report)
	}
}

func testHost(id ID, support Support, commands []string, paths []knownPath) Host {
	return Host{
		ID: id, DisplayName: string(id), DocumentationURL: "https://example.invalid/" + string(id),
		Surfaces: []Surface{SurfaceCLI}, Platforms: []string{"darwin", "linux", "windows"},
		Support: support, Detection: DetectionSpec{Commands: commands, paths: paths},
	}
}

func testCatalog(t *testing.T, hosts ...Host) Catalog {
	t.Helper()
	catalog, err := NewCatalog(hosts)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func testDetector(t *testing.T, catalog Catalog, options Options) *Detector {
	t.Helper()
	detector, err := NewDetector(catalog, options)
	if err != nil {
		t.Fatal(err)
	}
	return detector
}

func assertDetection(t *testing.T, report Report, id ID, status DetectionStatus, evidence Evidence) {
	t.Helper()
	for _, item := range report.Hosts {
		if item.Host.ID != id {
			continue
		}
		if item.Status != status || !slices.Contains(item.Evidence, evidence) {
			t.Fatalf("detection %q = %+v, want %q and %+v", id, item, status, evidence)
		}
		return
	}
	t.Fatalf("detection %q missing from %+v", id, report.Hosts)
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func pathBase(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	if index := strings.LastIndexByte(value, '/'); index >= 0 {
		return value[index+1:]
	}
	return value
}

var _ FileSystem = (*syntheticFS)(nil)
