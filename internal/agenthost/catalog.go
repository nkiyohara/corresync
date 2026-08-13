// Package agenthost describes local AI-agent surfaces and detects their
// installation without executing them or reading their configuration.
package agenthost

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// ID is the stable identifier shared by the CLI, onboarding, documentation,
// and future integration lifecycle adapters.
type ID string

const (
	IDCodex         ID = "codex"
	IDClaudeCode    ID = "claude-code"
	IDClaudeDesktop ID = "claude-desktop"
	IDGitHubCopilot ID = "github-copilot"
	IDVSCode        ID = "vscode"
	IDGeminiCLI     ID = "gemini-cli"
	IDKiro          ID = "kiro"
	IDQwenCode      ID = "qwen-code"
	IDQoder         ID = "qoder"
	IDKimiCode      ID = "kimi-code"
	IDCursor        ID = "cursor"
	IDWindsurf      ID = "windsurf"
	IDOpenCode      ID = "opencode"
	IDCline         ID = "cline"
	IDRooCode       ID = "roo-code"
	IDZed           ID = "zed"
	IDGoose         ID = "goose"
)

type Surface string

const (
	SurfaceCLI     Surface = "cli"
	SurfaceDesktop Surface = "desktop"
	SurfaceIDE     Surface = "ide"
)

type Scope string

const (
	ScopeUser      Scope = "user"
	ScopeProject   Scope = "project"
	ScopeWorkspace Scope = "workspace"
	ScopeLocal     Scope = "local"
)

type Support string

const (
	SupportVerified     Support = "verified"
	SupportExperimental Support = "experimental"
	SupportConfigOnly   Support = "config_only"
	SupportCatalogOnly  Support = "catalog_only"
)

type NativePackage string

const (
	NativeNone      NativePackage = ""
	NativePlugin    NativePackage = "plugin"
	NativeExtension NativePackage = "extension"
	NativePower     NativePackage = "power"
)

type Capabilities struct {
	LocalStdioMCP      bool          `json:"localStdioMcp"`
	AgentSkill         bool          `json:"agentSkill"`
	NativePackage      NativePackage `json:"nativePackage,omitempty"`
	SelfContainedMCPB  bool          `json:"selfContainedMcpb"`
	MarketplaceSurface bool          `json:"marketplaceSurface"`
	Published          bool          `json:"published"`
}

// Lifecycle records only behavior that exists today. Later adapters can add
// inspect/repair/remove support without changing catalog identity.
type Lifecycle struct {
	AdapterID      string `json:"adapterId,omitempty"`
	Setup          bool   `json:"setup"`
	Inspect        bool   `json:"inspect"`
	Verify         bool   `json:"verify"`
	Repair         bool   `json:"repair"`
	Remove         bool   `json:"remove"`
	ReloadRequired bool   `json:"reloadRequired"`
}

type DetectionSpec struct {
	Commands      []string       `json:"commands,omitempty"`
	EvidenceKinds []EvidenceKind `json:"evidenceKinds,omitempty"`
	paths         []knownPath
}

type Host struct {
	ID               ID            `json:"id"`
	DisplayName      string        `json:"displayName"`
	Aliases          []string      `json:"aliases,omitempty"`
	DocumentationURL string        `json:"documentationUrl"`
	Surfaces         []Surface     `json:"surfaces"`
	Platforms        []string      `json:"platforms"`
	Scopes           []Scope       `json:"scopes,omitempty"`
	Support          Support       `json:"support"`
	Capabilities     Capabilities  `json:"capabilities"`
	Lifecycle        Lifecycle     `json:"lifecycle"`
	Detection        DetectionSpec `json:"detection"`
}

type pathRoot string

const (
	rootHome            pathRoot = "home"
	rootConfig          pathRoot = "config"
	rootApplications    pathRoot = "applications"
	rootLocalAppData    pathRoot = "local_app_data"
	rootAppData         pathRoot = "app_data"
	rootProgramFiles    pathRoot = "program_files"
	rootProgramFilesX86 pathRoot = "program_files_x86"
)

type EvidenceKind string

const (
	EvidenceExecutable  EvidenceKind = "executable"
	EvidenceApplication EvidenceKind = "application"
	EvidenceConfig      EvidenceKind = "config_footprint"
)

type knownPath struct {
	platform string
	root     pathRoot
	kind     EvidenceKind
	parts    []string
}

func known(platform string, root pathRoot, kind EvidenceKind, parts ...string) knownPath {
	return knownPath{platform: platform, root: root, kind: kind, parts: parts}
}

type Catalog struct {
	hosts   []Host
	aliases map[string]int
}

// DefaultCatalog returns an independently owned catalog snapshot.
func DefaultCatalog() Catalog {
	catalog, err := NewCatalog(defaultHosts())
	if err != nil {
		panic("invalid built-in agent-host catalog: " + err.Error())
	}
	return catalog
}

func NewCatalog(hosts []Host) (Catalog, error) {
	if len(hosts) == 0 {
		return Catalog{}, errors.New("agent-host catalog is empty")
	}
	if len(hosts) > 64 {
		return Catalog{}, errors.New("agent-host catalog exceeds the 64-host detection bound")
	}
	catalog := Catalog{
		hosts:   cloneHosts(hosts),
		aliases: make(map[string]int, len(hosts)*2),
	}
	for index := range catalog.hosts {
		host := &catalog.hosts[index]
		host.Detection.EvidenceKinds = detectionKinds(host.Detection)
		if err := validateHost(*host); err != nil {
			return Catalog{}, fmt.Errorf("host %q: %w", host.ID, err)
		}
		for _, value := range append([]string{string(host.ID)}, host.Aliases...) {
			key := normalizeAlias(value)
			if _, exists := catalog.aliases[key]; exists {
				return Catalog{}, fmt.Errorf("duplicate host ID or alias %q", value)
			}
			catalog.aliases[key] = index
		}
	}
	return catalog, nil
}

func (catalog Catalog) Hosts() []Host {
	return cloneHosts(catalog.hosts)
}

func (catalog Catalog) Lookup(value string) (Host, bool) {
	index, ok := catalog.aliases[normalizeAlias(value)]
	if !ok {
		return Host{}, false
	}
	return cloneHost(catalog.hosts[index]), true
}

func normalizeAlias(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	commandPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
)

func validateHost(host Host) error {
	if !idPattern.MatchString(string(host.ID)) {
		return errors.New("ID must be lower-case letters, numbers, and hyphens")
	}
	if strings.TrimSpace(host.DisplayName) == "" || hasControl(host.DisplayName) {
		return errors.New("display name is empty or contains a control character")
	}
	parsed, err := url.Parse(host.DocumentationURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("documentation URL must be an absolute credential-free HTTPS URL")
	}
	if len(host.Surfaces) == 0 || len(host.Platforms) == 0 {
		return errors.New("surfaces and platforms are required")
	}
	if err := validateEnums(host.Surfaces, SurfaceCLI, SurfaceDesktop, SurfaceIDE); err != nil {
		return fmt.Errorf("surfaces: %w", err)
	}
	if err := validateEnums(host.Platforms, "darwin", "linux", "windows"); err != nil {
		return fmt.Errorf("platforms: %w", err)
	}
	if err := validateEnums(host.Scopes, ScopeUser, ScopeProject, ScopeWorkspace, ScopeLocal); err != nil {
		return fmt.Errorf("scopes: %w", err)
	}
	for _, alias := range host.Aliases {
		if !idPattern.MatchString(alias) {
			return fmt.Errorf("invalid alias %q", alias)
		}
	}
	switch host.Support {
	case SupportVerified, SupportExperimental, SupportConfigOnly, SupportCatalogOnly:
	default:
		return fmt.Errorf("invalid support status %q", host.Support)
	}
	switch host.Capabilities.NativePackage {
	case NativeNone, NativePlugin, NativeExtension, NativePower:
	default:
		return fmt.Errorf("invalid native package %q", host.Capabilities.NativePackage)
	}
	if len(host.Detection.Commands) > 4 || len(host.Detection.paths) > 8 {
		return errors.New("detection specification exceeds its per-host bound")
	}
	seenCommands := make(map[string]bool, len(host.Detection.Commands))
	for _, command := range host.Detection.Commands {
		if !commandPattern.MatchString(command) {
			return fmt.Errorf("unsafe command probe %q", command)
		}
		if seenCommands[strings.ToLower(command)] {
			return fmt.Errorf("duplicate command probe %q", command)
		}
		seenCommands[strings.ToLower(command)] = true
	}
	for _, path := range host.Detection.paths {
		if path.platform != "" && path.platform != "darwin" && path.platform != "linux" && path.platform != "windows" {
			return fmt.Errorf("invalid known-path platform %q", path.platform)
		}
		switch path.root {
		case rootHome, rootConfig, rootApplications, rootLocalAppData, rootAppData, rootProgramFiles, rootProgramFilesX86:
		default:
			return fmt.Errorf("invalid known-path root %q", path.root)
		}
		switch path.kind {
		case EvidenceExecutable, EvidenceApplication, EvidenceConfig:
		default:
			return fmt.Errorf("invalid known-path evidence kind %q", path.kind)
		}
		if len(path.parts) == 0 || len(path.parts) > 8 {
			return errors.New("known path has an invalid component count")
		}
		for _, part := range path.parts {
			if part == "" || part == "." || part == ".." || hasControl(part) || strings.ContainsAny(part, `/\\`) {
				return fmt.Errorf("unsafe known-path component %q", part)
			}
		}
	}
	if host.Lifecycle.AdapterID != "" && !idPattern.MatchString(host.Lifecycle.AdapterID) {
		return fmt.Errorf("invalid lifecycle adapter ID %q", host.Lifecycle.AdapterID)
	}
	if host.Lifecycle.Setup {
		if host.Lifecycle.AdapterID == "" || !host.Lifecycle.Inspect || !host.Lifecycle.Verify ||
			!host.Lifecycle.Repair || !host.Lifecycle.Remove {
			return errors.New("setup lifecycle requires complete inspect, verify, repair, and remove support")
		}
	}
	return nil
}

func validateEnums[T comparable](values []T, allowed ...T) error {
	seen := make(map[T]bool, len(values))
	for _, value := range values {
		if seen[value] {
			return fmt.Errorf("duplicate value %v", value)
		}
		if !slices.Contains(allowed, value) {
			return fmt.Errorf("invalid value %v", value)
		}
		seen[value] = true
	}
	return nil
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

func cloneHosts(hosts []Host) []Host {
	cloned := make([]Host, len(hosts))
	for index := range hosts {
		cloned[index] = cloneHost(hosts[index])
	}
	return cloned
}

func cloneHost(host Host) Host {
	host.Aliases = slices.Clone(host.Aliases)
	host.Surfaces = slices.Clone(host.Surfaces)
	host.Platforms = slices.Clone(host.Platforms)
	host.Scopes = slices.Clone(host.Scopes)
	host.Detection.Commands = slices.Clone(host.Detection.Commands)
	host.Detection.EvidenceKinds = slices.Clone(host.Detection.EvidenceKinds)
	host.Detection.paths = slices.Clone(host.Detection.paths)
	for index := range host.Detection.paths {
		host.Detection.paths[index].parts = slices.Clone(host.Detection.paths[index].parts)
	}
	return host
}

func detectionKinds(spec DetectionSpec) []EvidenceKind {
	kinds := make([]EvidenceKind, 0, 3)
	if len(spec.Commands) > 0 {
		kinds = append(kinds, EvidenceExecutable)
	}
	for _, candidate := range spec.paths {
		if !slices.Contains(kinds, candidate.kind) {
			kinds = append(kinds, candidate.kind)
		}
	}
	slices.Sort(kinds)
	return kinds
}

func setupLifecycle(adapter string) Lifecycle {
	return Lifecycle{
		AdapterID: adapter, Setup: true, Inspect: true, Verify: true,
		Repair: true, Remove: true, ReloadRequired: true,
	}
}

func defaultHosts() []Host {
	all := []string{"darwin", "linux", "windows"}
	cli := []Surface{SurfaceCLI}
	ide := []Surface{SurfaceIDE}
	cliIDE := []Surface{SurfaceCLI, SurfaceIDE}
	user := []Scope{ScopeUser}
	userProject := []Scope{ScopeUser, ScopeProject}
	localProjectUser := []Scope{ScopeLocal, ScopeProject, ScopeUser}
	verifiedMCP := Capabilities{LocalStdioMCP: true}

	return []Host{
		{
			ID: IDCodex, DisplayName: "Codex", Aliases: []string{"openai-codex"},
			DocumentationURL: "https://developers.openai.com/codex/", Surfaces: cli, Platforms: all,
			Scopes: user, Support: SupportVerified,
			Capabilities: Capabilities{LocalStdioMCP: true, AgentSkill: true, NativePackage: NativePlugin, MarketplaceSurface: true},
			Lifecycle:    setupLifecycle("codex-cli"),
			Detection:    DetectionSpec{Commands: []string{"codex"}, paths: []knownPath{known("", rootHome, EvidenceConfig, ".codex")}},
		},
		{
			ID: IDClaudeCode, DisplayName: "Claude Code", Aliases: []string{"claude"},
			DocumentationURL: "https://code.claude.com/docs/en/overview", Surfaces: cli, Platforms: all,
			Scopes: localProjectUser, Support: SupportVerified,
			Capabilities: Capabilities{LocalStdioMCP: true, AgentSkill: true, NativePackage: NativePlugin, MarketplaceSurface: true},
			Lifecycle:    setupLifecycle("claude-code-cli"),
			Detection:    DetectionSpec{Commands: []string{"claude"}, paths: []knownPath{known("", rootHome, EvidenceConfig, ".claude")}},
		},
		{
			ID: IDClaudeDesktop, DisplayName: "Claude Desktop",
			DocumentationURL: "https://support.claude.com/en/articles/10949351-getting-started-with-local-mcp-servers-on-claude-desktop",
			Surfaces:         []Surface{SurfaceDesktop}, Platforms: []string{"darwin", "windows"},
			Scopes: user, Support: SupportVerified,
			Capabilities: Capabilities{LocalStdioMCP: true, SelfContainedMCPB: true},
			Lifecycle:    Lifecycle{AdapterID: "claude-desktop-mcpb", ReloadRequired: true},
			Detection: DetectionSpec{paths: []knownPath{
				known("darwin", rootApplications, EvidenceApplication, "Claude.app"),
				known("darwin", rootConfig, EvidenceConfig, "Claude", "claude_desktop_config.json"),
				known("windows", rootConfig, EvidenceConfig, "Claude", "claude_desktop_config.json"),
			}},
		},
		{
			ID: IDGitHubCopilot, DisplayName: "GitHub Copilot CLI", Aliases: []string{"copilot", "copilot-cli"},
			DocumentationURL: "https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli", Surfaces: cli, Platforms: all,
			Scopes: user, Support: SupportVerified,
			Capabilities: Capabilities{LocalStdioMCP: true, AgentSkill: true, NativePackage: NativePlugin, MarketplaceSurface: true},
			Lifecycle:    setupLifecycle("github-copilot-cli"),
			Detection:    DetectionSpec{Commands: []string{"copilot"}, paths: []knownPath{known("", rootHome, EvidenceConfig, ".copilot")}},
		},
		{
			ID: IDVSCode, DisplayName: "Visual Studio Code", Aliases: []string{"code"},
			DocumentationURL: "https://code.visualstudio.com/docs/agent-customization/agent-plugins", Surfaces: ide, Platforms: all,
			Scopes: []Scope{ScopeUser, ScopeWorkspace}, Support: SupportExperimental,
			Capabilities: Capabilities{LocalStdioMCP: true, AgentSkill: true, NativePackage: NativePlugin, MarketplaceSurface: true},
			Lifecycle:    setupLifecycle("vscode-workspace-config"),
			Detection: DetectionSpec{Commands: []string{"code"}, paths: []knownPath{
				known("darwin", rootApplications, EvidenceApplication, "Visual Studio Code.app"),
				known("windows", rootLocalAppData, EvidenceApplication, "Programs", "Microsoft VS Code", "Code.exe"),
			}},
		},
		{
			ID: IDGeminiCLI, DisplayName: "Gemini CLI", Aliases: []string{"gemini"},
			DocumentationURL: "https://geminicli.com/docs/", Surfaces: cli, Platforms: all,
			Scopes: userProject, Support: SupportVerified,
			Capabilities: Capabilities{LocalStdioMCP: true, AgentSkill: true, NativePackage: NativeExtension, MarketplaceSurface: true},
			Lifecycle:    setupLifecycle("gemini-cli"),
			Detection:    DetectionSpec{Commands: []string{"gemini"}, paths: []knownPath{known("", rootHome, EvidenceConfig, ".gemini")}},
		},
		{
			ID: IDKiro, DisplayName: "Kiro IDE", Aliases: []string{"kiro-ide"},
			DocumentationURL: "https://kiro.dev/docs/", Surfaces: ide, Platforms: all,
			Scopes: []Scope{ScopeUser, ScopeWorkspace}, Support: SupportExperimental,
			Capabilities: Capabilities{LocalStdioMCP: true, AgentSkill: true, NativePackage: NativePower, MarketplaceSurface: true},
			Lifecycle:    Lifecycle{AdapterID: "kiro-power", ReloadRequired: true},
			Detection: DetectionSpec{Commands: []string{"kiro"}, paths: []knownPath{
				known("darwin", rootApplications, EvidenceApplication, "Kiro.app"),
				known("windows", rootLocalAppData, EvidenceApplication, "Programs", "Kiro", "Kiro.exe"),
				known("", rootHome, EvidenceConfig, ".kiro"),
			}},
		},
		{
			ID: IDQwenCode, DisplayName: "Qwen Code", Aliases: []string{"qwen"},
			DocumentationURL: "https://qwenlm.github.io/qwen-code-docs/en/", Surfaces: cli, Platforms: all,
			Scopes: userProject, Support: SupportVerified, Capabilities: verifiedMCP,
			Lifecycle: setupLifecycle("qwen-code-cli"),
			Detection: DetectionSpec{Commands: []string{"qwen"}, paths: []knownPath{known("", rootHome, EvidenceConfig, ".qwen")}},
		},
		{
			ID: IDQoder, DisplayName: "Qoder", Aliases: []string{"qodercli"},
			DocumentationURL: "https://docs.qoder.com/", Surfaces: cliIDE, Platforms: all,
			Scopes: localProjectUser, Support: SupportVerified, Capabilities: verifiedMCP,
			Lifecycle: setupLifecycle("qoder-cli"),
			Detection: DetectionSpec{Commands: []string{"qodercli"}, paths: []knownPath{
				known("darwin", rootApplications, EvidenceApplication, "Qoder.app"),
				known("", rootHome, EvidenceConfig, ".qoder"),
			}},
		},
		{
			ID: IDKimiCode, DisplayName: "Kimi Code CLI", Aliases: []string{"kimi", "kimi-cli"},
			DocumentationURL: "https://moonshotai.github.io/kimi-cli/", Surfaces: cli, Platforms: all,
			Scopes: user, Support: SupportConfigOnly, Capabilities: verifiedMCP,
			Lifecycle: setupLifecycle("kimi-code-cli"),
			Detection: DetectionSpec{Commands: []string{"kimi"}, paths: []knownPath{known("", rootHome, EvidenceConfig, ".kimi")}},
		},
		experimentalHost(IDCursor, "Cursor", []string{"cursor-agent"}, "https://docs.cursor.com/context/model-context-protocol", cliIDE, []string{"cursor", "cursor-agent"}, []knownPath{
			known("darwin", rootApplications, EvidenceApplication, "Cursor.app"),
			known("windows", rootLocalAppData, EvidenceApplication, "Programs", "cursor", "Cursor.exe"),
			known("", rootHome, EvidenceConfig, ".cursor"),
		}),
		experimentalHost(IDWindsurf, "Windsurf", []string{"cascade"}, "https://docs.windsurf.com/windsurf/cascade/mcp", cliIDE, []string{"windsurf"}, []knownPath{
			known("darwin", rootApplications, EvidenceApplication, "Windsurf.app"),
			known("windows", rootProgramFiles, EvidenceApplication, "Windsurf", "Windsurf.exe"),
			known("", rootHome, EvidenceConfig, ".codeium", "windsurf"),
		}),
		experimentalHost(IDOpenCode, "OpenCode", nil, "https://opencode.ai/v2/docs/mcp-servers", cli, []string{"opencode", "opencode2"}, []knownPath{known("", rootConfig, EvidenceConfig, "opencode")}),
		experimentalHost(IDCline, "Cline", nil, "https://docs.cline.bot/mcp/mcp-overview", cliIDE, []string{"cline"}, nil),
		experimentalHost(IDRooCode, "Roo Code", []string{"roo"}, "https://docs.roocode.com/features/mcp/overview", ide, nil, nil),
		experimentalHost(IDZed, "Zed", nil, "https://zed.dev/docs/ai/mcp", ide, nil, []knownPath{
			known("darwin", rootApplications, EvidenceApplication, "Zed.app"),
			known("", rootConfig, EvidenceConfig, "zed"),
		}),
		experimentalHost(IDGoose, "goose", nil, "https://block.github.io/goose/", []Surface{SurfaceCLI, SurfaceDesktop}, []string{"goose"}, []knownPath{
			known("darwin", rootApplications, EvidenceApplication, "Goose.app"),
			known("", rootConfig, EvidenceConfig, "goose"),
		}),
		catalogOnlyHost("aider", "Aider", nil, "https://aider.chat/docs/", cli, "aider"),
		catalogOnlyHost("amp", "Amp", nil, "https://ampcode.com/manual", cli, "amp"),
		catalogOnlyHost("augment", "Augment Code", nil, "https://docs.augmentcode.com/", ide),
		catalogOnlyHost("continue", "Continue", nil, "https://docs.continue.dev/", ide),
		catalogOnlyHost("crush", "Crush", nil, "https://github.com/charmbracelet/crush", cli, "crush"),
		catalogOnlyHost("devin", "Devin", nil, "https://docs.devin.ai/", cli, "devin"),
		catalogOnlyHost("droid", "Droid", []string{"factory-droid"}, "https://docs.factory.ai/cli/", cli, "droid"),
		catalogOnlyHost("grok", "Grok", nil, "https://docs.x.ai/", cli),
		catalogOnlyHost("hermes", "Hermes Agent", []string{"hermes-agent"}, "https://github.com/NousResearch/hermes-agent", cli, "hermes"),
		catalogOnlyHost("kilo", "Kilo Code", []string{"kilo-code"}, "https://kilo.ai/docs", ide),
		catalogOnlyHost("mistral-vibe", "Mistral Vibe", []string{"vibe"}, "https://docs.mistral.ai/mistral-vibe/", cli, "vibe"),
		catalogOnlyHost("trae", "TRAE", []string{"trae-ide"}, "https://docs.trae.ai/", ide),
	}
}

func experimentalHost(
	id ID,
	display string,
	aliases []string,
	documentation string,
	surfaces []Surface,
	commands []string,
	paths []knownPath,
) Host {
	return Host{
		ID: id, DisplayName: display, Aliases: aliases, DocumentationURL: documentation,
		Surfaces: surfaces, Platforms: []string{"darwin", "linux", "windows"},
		Scopes: experimentalScopes(id), Support: SupportExperimental,
		Capabilities: Capabilities{
			LocalStdioMCP: true, AgentSkill: experimentalSkill(id), MarketplaceSurface: true,
		},
		Lifecycle: experimentalLifecycle(id),
		Detection: DetectionSpec{Commands: commands, paths: paths},
	}
}

func experimentalScopes(id ID) []Scope {
	//nolint:exhaustive // Only experimental hosts with a non-user scope need an override.
	switch id {
	case IDVSCode:
		return []Scope{ScopeUser, ScopeWorkspace}
	case IDCursor, IDOpenCode, IDZed:
		return []Scope{ScopeUser, ScopeProject}
	case IDRooCode:
		return []Scope{ScopeProject}
	default:
		return []Scope{ScopeUser}
	}
}

func experimentalSkill(id ID) bool {
	//nolint:exhaustive // The portable Skill is advertised only for explicitly documented hosts.
	switch id {
	case IDVSCode, IDOpenCode, IDCline, IDZed:
		return true
	default:
		return false
	}
}

func experimentalLifecycle(id ID) Lifecycle {
	//nolint:exhaustive // Hosts without a reviewed adapter intentionally keep the planned lifecycle.
	switch id {
	case IDCursor, IDWindsurf, IDOpenCode, IDCline, IDRooCode, IDZed, IDGoose:
		return setupLifecycle(string(id) + "-config")
	default:
		return Lifecycle{AdapterID: string(id) + "-adapter", ReloadRequired: true}
	}
}

func catalogOnlyHost(
	id ID,
	display string,
	aliases []string,
	documentation string,
	surfaces []Surface,
	commands ...string,
) Host {
	return Host{
		ID: id, DisplayName: display, Aliases: aliases, DocumentationURL: documentation,
		Surfaces: surfaces, Platforms: []string{"darwin", "linux", "windows"},
		Scopes: []Scope{ScopeUser}, Support: SupportCatalogOnly,
		Detection: DetectionSpec{Commands: commands},
	}
}
