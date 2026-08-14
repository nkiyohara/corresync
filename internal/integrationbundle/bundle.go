// Package integrationbundle owns the reviewed identity and launch contract
// shared by Corresync integration packages. Renderers deliberately emit only
// public metadata and a local stdio command; account state and credentials are
// outside the representable model.
package integrationbundle

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	SkillPath        = "plugins/corresync/skills/corresync/SKILL.md"
	IconPath         = "plugins/corresync/assets/icon.svg"
	maxVersionLength = 64

	mcpbSchema = "https://raw.githubusercontent.com/modelcontextprotocol/mcpb/70fe3b34cd6dff1b3bba046638edc72a6467a4fb/schemas/mcpb-manifest-v0.4.schema.json"
)

//go:embed spec.json
var specification []byte

var (
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

type Launch struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Transport string   `json:"transport"`
}

type Effects struct {
	ReadsPrivateData           bool   `json:"readsPrivateData"`
	WritesRequirePreviewCommit bool   `json:"writesRequirePreviewCommit"`
	AutoApproval               bool   `json:"autoApproval"`
	NetworkScope               string `json:"networkScope"`
	SecretPolicy               string `json:"secretPolicy"`
}

type Surface struct {
	ID         string `json:"id"`
	Package    string `json:"package"`
	Mode       string `json:"mode"`
	Skill      bool   `json:"skill"`
	LocalMCP   bool   `json:"localMcp"`
	Limitation string `json:"limitation"`
}

// PublicationChannel describes one external distribution or discovery
// surface. Source availability and directory publication are separate states:
// a checked-in native package must never imply an upstream listing.
type PublicationChannel struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"displayName"`
	PackageFormat     string   `json:"packageFormat"`
	SupportedSurfaces []string `json:"supportedSurfaces"`
	Owner             string   `json:"owner"`
	PublishMethod     string   `json:"publishMethod"`
	State             string   `json:"state"`
	ObservedVersion   string   `json:"observedVersion,omitempty"`
	SourceVersioned   bool     `json:"sourceVersioned,omitempty"`
	VerificationURL   string   `json:"verificationUrl"`
	SubmissionURL     string   `json:"submissionUrl"`
	InstallBehavior   string   `json:"installBehavior"`
	Limitation        string   `json:"limitation"`
}

type Spec struct {
	SchemaVersion       int                  `json:"schemaVersion"`
	SourceVersion       string               `json:"sourceVersion"`
	ID                  string               `json:"id"`
	RegistryName        string               `json:"registryName"`
	DisplayName         string               `json:"displayName"`
	Title               string               `json:"title"`
	Description         string               `json:"description"`
	ShortDescription    string               `json:"shortDescription"`
	LongDescription     string               `json:"longDescription"`
	Author              string               `json:"author"`
	Repository          string               `json:"repository"`
	RepositoryGit       string               `json:"repositoryGit"`
	Homepage            string               `json:"homepage"`
	Documentation       string               `json:"documentation"`
	Privacy             string               `json:"privacy"`
	Support             string               `json:"support"`
	License             string               `json:"license"`
	Category            string               `json:"category"`
	BrandColor          string               `json:"brandColor"`
	Keywords            []string             `json:"keywords"`
	Launch              Launch               `json:"launch"`
	Requirements        []string             `json:"requirements"`
	Effects             Effects              `json:"effects"`
	Surfaces            []Surface            `json:"surfaces"`
	ConfigOnlyHosts     []string             `json:"configOnlyHosts"`
	PublicationChannels []PublicationChannel `json:"publicationChannels"`
}

type Inputs struct {
	Skill []byte
	Icon  []byte
}

type Output struct {
	Path string
	Data []byte
}

type registryDocument struct {
	Schema      string `json:"$schema"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Repository  struct {
		URL    string `json:"url"`
		Source string `json:"source"`
	} `json:"repository"`
	WebsiteURL string `json:"websiteUrl"`
	Packages   []struct {
		RegistryType string `json:"registryType"`
		Identifier   string `json:"identifier"`
		FileSHA256   string `json:"fileSha256"`
		Transport    struct {
			Type string `json:"type"`
		} `json:"transport"`
	} `json:"packages"`
}

func Load() (Spec, error) {
	var spec Spec
	decoder := json.NewDecoder(bytes.NewReader(specification))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("decode integration bundle specification: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return Spec{}, fmt.Errorf("decode integration bundle specification: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, fmt.Errorf("invalid integration bundle specification: %w", err)
	}
	return spec, nil
}

func MustLoad() Spec {
	spec, err := Load()
	if err != nil {
		panic(err)
	}
	return spec
}

func (spec Spec) Validate() error {
	if spec.SchemaVersion != 2 {
		return fmt.Errorf("unsupported schema version %d", spec.SchemaVersion)
	}
	if len(spec.SourceVersion) > maxVersionLength || !versionPattern.MatchString(spec.SourceVersion) {
		return fmt.Errorf("source version %q is not SemVer", spec.SourceVersion)
	}
	required := map[string]string{
		"id": spec.ID, "registryName": spec.RegistryName, "displayName": spec.DisplayName,
		"title": spec.Title, "description": spec.Description, "shortDescription": spec.ShortDescription,
		"longDescription": spec.LongDescription, "author": spec.Author, "license": spec.License,
		"category": spec.Category, "brandColor": spec.BrandColor,
		"networkScope": spec.Effects.NetworkScope, "secretPolicy": spec.Effects.SecretPolicy,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" || hasControl(value) {
			return fmt.Errorf("%s is empty or contains a control character", name)
		}
	}
	if spec.ID != "corresync" || !idPattern.MatchString(spec.ID) || spec.RegistryName != "io.github.nkiyohara/corresync" {
		return errors.New("stable product and registry IDs changed")
	}
	if utf8.RuneCountInString(spec.Description) > 100 {
		return errors.New("registry description exceeds the 100-character publication limit")
	}
	for name, value := range map[string]string{
		"repository": spec.Repository, "repositoryGit": spec.RepositoryGit,
		"homepage": spec.Homepage, "documentation": spec.Documentation,
		"privacy": spec.Privacy, "support": spec.Support,
	} {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return fmt.Errorf("%s must be an absolute credential-free HTTPS URL", name)
		}
	}
	if spec.Launch.Command != "corr" || !slices.Equal(spec.Launch.Args, []string{"mcp", "serve"}) || spec.Launch.Transport != "stdio" {
		return errors.New("portable launch contract must be corr mcp serve over stdio")
	}
	if spec.Effects.AutoApproval {
		return errors.New("integration bundles cannot enable automatic approval")
	}
	if !spec.Effects.WritesRequirePreviewCommit || strings.TrimSpace(spec.Effects.SecretPolicy) == "" {
		return errors.New("guarded writes and the no-secret policy must be explicit")
	}
	if len(spec.Keywords) == 0 || len(spec.Requirements) == 0 || len(spec.Surfaces) == 0 ||
		len(spec.ConfigOnlyHosts) == 0 || len(spec.PublicationChannels) == 0 {
		return errors.New("keywords, requirements, integration surfaces, config-only hosts, and publication channels are required")
	}
	for name, values := range map[string][]string{"keywords": spec.Keywords, "requirements": spec.Requirements} {
		for index, value := range values {
			if strings.TrimSpace(value) == "" || hasControl(value) {
				return fmt.Errorf("%s[%d] is empty or contains a control character", name, index)
			}
		}
	}
	seen := make(map[string]bool, len(spec.Surfaces)+len(spec.ConfigOnlyHosts))
	for _, surface := range spec.Surfaces {
		if !idPattern.MatchString(surface.ID) || !idPattern.MatchString(surface.Package) ||
			strings.TrimSpace(surface.Limitation) == "" || hasControl(surface.Limitation) {
			return errors.New("surface ID, package, and limitation are required")
		}
		if surface.Mode != "thin" && surface.Mode != "self-contained" {
			return fmt.Errorf("surface %q has invalid package mode %q", surface.ID, surface.Mode)
		}
		if seen[surface.ID] {
			return fmt.Errorf("duplicate surface %q", surface.ID)
		}
		seen[surface.ID] = true
	}
	for _, host := range spec.ConfigOnlyHosts {
		if !idPattern.MatchString(host) || seen[host] {
			return fmt.Errorf("invalid or duplicate config-only host %q", host)
		}
		seen[host] = true
	}
	channelIDs := make(map[string]bool, len(spec.PublicationChannels))
	for _, channel := range spec.PublicationChannels {
		if !idPattern.MatchString(channel.ID) || channelIDs[channel.ID] {
			return fmt.Errorf("invalid or duplicate publication channel %q", channel.ID)
		}
		channelIDs[channel.ID] = true
		for name, value := range map[string]string{
			"displayName": channel.DisplayName, "packageFormat": channel.PackageFormat,
			"owner": channel.Owner, "publishMethod": channel.PublishMethod,
			"installBehavior": channel.InstallBehavior, "limitation": channel.Limitation,
		} {
			if strings.TrimSpace(value) == "" || hasControl(value) {
				return fmt.Errorf("publication channel %q %s is empty or contains a control character", channel.ID, name)
			}
		}
		if len(channel.SupportedSurfaces) == 0 {
			return fmt.Errorf("publication channel %q has no supported surface", channel.ID)
		}
		for _, surface := range channel.SupportedSurfaces {
			switch surface {
			case "local-cli", "local-desktop", "local-ide":
			default:
				return fmt.Errorf("publication channel %q has unsupported surface %q", channel.ID, surface)
			}
		}
		switch channel.State {
		case "published":
			if !versionPattern.MatchString(channel.ObservedVersion) || channel.SourceVersioned {
				return fmt.Errorf("published channel %q must have one observed stable version", channel.ID)
			}
		case "source-available":
			if channel.ObservedVersion != "" || !channel.SourceVersioned {
				return fmt.Errorf("source channel %q must take the generated source version", channel.ID)
			}
		case "not-listed":
			if channel.ObservedVersion != "" || channel.SourceVersioned {
				return fmt.Errorf("unlisted channel %q cannot claim a version", channel.ID)
			}
		default:
			return fmt.Errorf("publication channel %q has invalid state %q", channel.ID, channel.State)
		}
		for name, value := range map[string]string{
			"verificationUrl": channel.VerificationURL,
			"submissionUrl":   channel.SubmissionURL,
		} {
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
				return fmt.Errorf("publication channel %q %s must be an absolute credential-free HTTPS URL", channel.ID, name)
			}
		}
	}
	return nil
}

// PublicationChannels returns an independently owned snapshot with any
// source-versioned channel resolved to version.
func (spec Spec) PublicationSnapshot(version string) ([]PublicationChannel, error) {
	if len(version) > maxVersionLength || !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("invalid publication snapshot version %q", version)
	}
	channels := make([]PublicationChannel, len(spec.PublicationChannels))
	for index, channel := range spec.PublicationChannels {
		channels[index] = channel
		channels[index].SupportedSurfaces = slices.Clone(channel.SupportedSurfaces)
		if channel.SourceVersioned {
			channels[index].ObservedVersion = version
		}
	}
	return channels, nil
}

func SourceVersion() string { return MustLoad().SourceVersion }

func PortableLaunch() Launch {
	launch := MustLoad().Launch
	launch.Args = slices.Clone(launch.Args)
	return launch
}

// Render returns every checked-in generated integration asset in path order.
func Render(version string, inputs Inputs) ([]Output, error) {
	spec, err := Load()
	if err != nil {
		return nil, err
	}
	if len(version) > maxVersionLength || !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("invalid integration bundle version %q", version)
	}
	if err := validateSkill(inputs.Skill, spec.ID); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(inputs.Icon)) == 0 {
		return nil, errors.New("integration icon is empty")
	}

	commonMCP := map[string]any{
		"mcpServers": map[string]any{
			spec.ID: map[string]any{"command": spec.Launch.Command, "args": spec.Launch.Args},
		},
	}
	outputs := make([]Output, 0, 13)
	addJSON := func(path string, value any) error {
		data, marshalErr := marshalJSON(value)
		if marshalErr != nil {
			return fmt.Errorf("render %s: %w", path, marshalErr)
		}
		outputs = append(outputs, Output{Path: path, Data: data})
		return nil
	}

	if err := addJSON(".agents/plugins/marketplace.json", codexMarketplace(spec)); err != nil {
		return nil, err
	}
	if err := addJSON(".claude-plugin/marketplace.json", claudeMarketplace(spec, version)); err != nil {
		return nil, err
	}
	if err := addJSON("plugins/corresync/.codex-plugin/plugin.json", codexManifest(spec, version)); err != nil {
		return nil, err
	}
	if err := addJSON("plugins/corresync/.claude-plugin/plugin.json", claudeManifest(spec, version)); err != nil {
		return nil, err
	}
	if err := addJSON("plugins/corresync/.mcp.json", commonMCP); err != nil {
		return nil, err
	}
	if err := addJSON("integrations/gemini-cli/corresync/gemini-extension.json", geminiManifest(spec, version)); err != nil {
		return nil, err
	}
	outputs = append(outputs, Output{
		Path: "integrations/gemini-cli/corresync/skills/corresync/SKILL.md",
		Data: normalizeText(inputs.Skill),
	})
	if err := addJSON("integrations/kiro/corresync/mcp.json", commonMCP); err != nil {
		return nil, err
	}
	outputs = append(outputs,
		Output{Path: "integrations/kiro/corresync/POWER.md", Data: renderKiroPower(spec, version)},
		Output{Path: "integrations/kiro/corresync/steering/corresync.md", Data: renderKiroSteering(inputs.Skill)},
	)
	if err := addJSON("integrations/config-hosts.json", configHosts(spec, version)); err != nil {
		return nil, err
	}
	outputs = append(outputs,
		Output{Path: "docs/generated/integration-bundles.md", Data: renderDocumentation(spec, version)},
		Output{Path: "docs/generated/publication-channels.md", Data: renderPublicationDocumentation(spec, version)},
	)

	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Path < outputs[j].Path })
	return outputs, nil
}

func RenderMCPBManifest(version string) ([]byte, error) {
	spec, err := Load()
	if err != nil {
		return nil, err
	}
	if len(version) > maxVersionLength || !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("invalid integration bundle version %q", version)
	}
	manifest := map[string]any{
		"$schema":          mcpbSchema,
		"manifest_version": "0.4",
		"name":             spec.ID,
		"display_name":     spec.DisplayName,
		"version":          version,
		"description":      "Local-first mail, calendar, and task MCP server for accounts you control.",
		"long_description": spec.LongDescription + " Install the Corresync CLI separately to configure and authenticate accounts; the bundle never hosts or relays mailbox data.",
		"author":           map[string]any{"name": spec.Author, "url": spec.Repository},
		"repository":       map[string]any{"type": "git", "url": spec.RepositoryGit},
		"homepage":         spec.Homepage,
		"documentation":    spec.Documentation,
		"support":          spec.Support,
		"icon":             "icon.png",
		"server": map[string]any{
			"type": "binary", "entry_point": "server/launch.sh",
			"mcp_config": map[string]any{
				"command": "${__dirname}/server/launch.sh", "args": []string{}, "env": map[string]string{},
				"platform_overrides": map[string]any{
					"win32": map[string]any{"command": "cmd.exe", "args": []string{"/d", "/s", "/c", "\"${__dirname}/server/launch.cmd\""}},
				},
			},
		},
		"tools_generated":  true,
		"keywords":         []string{"mail", "calendar", "tasks", "productivity", "local-first", "privacy", "mcp", "stdio"},
		"license":          spec.License,
		"privacy_policies": []string{spec.Privacy},
		"compatibility":    map[string]any{"platforms": []string{"darwin", "linux", "win32"}},
	}
	return marshalJSON(manifest)
}

func RenderRegistryManifest(version, sha256 string) ([]byte, error) {
	spec, err := Load()
	if err != nil {
		return nil, err
	}
	if len(version) > maxVersionLength || !versionPattern.MatchString(version) {
		return nil, fmt.Errorf("invalid registry version %q", version)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sha256) {
		return nil, errors.New("registry MCPB SHA-256 must be 64 lower-case hexadecimal characters")
	}
	manifest := map[string]any{
		"$schema": "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
		"name":    spec.RegistryName, "title": spec.Title, "description": spec.Description,
		"version":    version,
		"repository": map[string]any{"url": spec.Repository, "source": "github"},
		"websiteUrl": spec.Homepage,
		"packages": []any{map[string]any{
			"registryType": "mcpb",
			"identifier":   fmt.Sprintf("%s/releases/download/v%s/%s_%s.mcpb", spec.Repository, version, spec.ID, version),
			"fileSha256":   sha256,
			"transport":    map[string]string{"type": spec.Launch.Transport},
		}},
	}
	data, err := marshalJSON(manifest)
	if err != nil {
		return nil, err
	}
	if err := ValidateRegistryManifest(data); err != nil {
		return nil, fmt.Errorf("validate rendered registry manifest: %w", err)
	}
	return data, nil
}

// ValidateRegistryManifest checks published or staged MCP Registry metadata
// against the canonical identity without requiring it to match the source
// snapshot version. A published root manifest legitimately follows the latest
// externally available release, while release staging uses the candidate tag.
func ValidateRegistryManifest(data []byte) error {
	spec, err := Load()
	if err != nil {
		return err
	}
	var document registryDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode MCP Registry manifest: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return fmt.Errorf("decode MCP Registry manifest: %w", err)
	}
	if document.Schema != "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json" ||
		document.Name != spec.RegistryName || document.Title != spec.Title ||
		document.Description != spec.Description || document.Repository.URL != spec.Repository ||
		document.Repository.Source != "github" || document.WebsiteURL != spec.Homepage {
		return errors.New("MCP Registry manifest identity differs from the canonical integration bundle")
	}
	if len(document.Version) > maxVersionLength || !versionPattern.MatchString(document.Version) {
		return fmt.Errorf("MCP Registry manifest version %q is not SemVer", document.Version)
	}
	if len(document.Packages) != 1 {
		return fmt.Errorf("MCP Registry manifest has %d packages, want one", len(document.Packages))
	}
	item := document.Packages[0]
	if item.RegistryType != "mcpb" || item.Transport.Type != spec.Launch.Transport ||
		!sha256Pattern.MatchString(item.FileSHA256) {
		return errors.New("MCP Registry package must bind one SHA-256 MCPB over stdio")
	}
	want := fmt.Sprintf("%s/releases/download/v%s/%s_%s.mcpb", spec.Repository, document.Version, spec.ID, document.Version)
	if item.Identifier != want {
		return fmt.Errorf("MCP Registry package URL is %q, want %q", item.Identifier, want)
	}
	return nil
}

func codexMarketplace(spec Spec) map[string]any {
	return map[string]any{
		"name":      spec.ID,
		"interface": map[string]string{"displayName": spec.DisplayName},
		"plugins": []any{map[string]any{
			"name":     spec.ID,
			"source":   map[string]string{"source": "local", "path": "./plugins/corresync"},
			"policy":   map[string]string{"installation": "AVAILABLE", "authentication": "ON_INSTALL"},
			"category": spec.Category,
		}},
	}
}

func claudeMarketplace(spec Spec, version string) map[string]any {
	return map[string]any{
		"name":        spec.ID,
		"owner":       map[string]string{"name": spec.Author},
		"description": "Natural-language discovery for the guarded local Corresync MCP server.",
		"version":     version,
		"plugins": []any{map[string]any{
			"name": spec.ID, "displayName": spec.DisplayName, "source": "./plugins/corresync",
			"description": "Recognize mail, calendar, and task requests and route them to Corresync.",
			"version":     version, "author": map[string]string{"name": spec.Author},
			"homepage": spec.Homepage, "repository": spec.Repository, "license": spec.License,
			"category": strings.ToLower(spec.Category), "tags": []string{"outlook", "email", "calendar", "tasks", "mcp"},
		}},
	}
}

func codexManifest(spec Spec, version string) map[string]any {
	return map[string]any{
		"name": spec.ID, "version": version,
		"description": "Makes guarded local mail, calendar, and task tools discoverable from natural-language requests.",
		"author":      map[string]string{"name": spec.Author, "url": spec.Repository},
		"homepage":    spec.Homepage, "repository": spec.Repository, "license": spec.License,
		"keywords": spec.Keywords, "skills": "./skills/", "mcpServers": "./.mcp.json",
		"interface": map[string]any{
			"displayName": spec.DisplayName, "shortDescription": spec.ShortDescription,
			"longDescription": "For local Codex sessions, recognizes inbox, email, calendar, schedule, meeting, reminder, and task requests and routes them to the guarded local Corresync MCP server. Hosted ChatGPT cannot reach this local stdio integration.",
			"developerName":   spec.Author, "category": spec.Category,
			"capabilities": []string{"Multi-account mail", "Multi-account calendar", "Multi-account tasks", "Reviewed writes", "Local monitoring"},
			"websiteURL":   spec.Homepage, "brandColor": spec.BrandColor,
			"composerIcon": "./assets/icon.svg", "logo": "./assets/icon.svg",
			"defaultPrompt": []string{
				"Check all my inboxes and summarize what needs attention.",
				"Show my calendars for today.",
				"Show my open tasks across configured accounts.",
				"Find the latest email about this project across my accounts.",
			},
		},
	}
}

func claudeManifest(spec Spec, version string) map[string]any {
	return map[string]any{
		"name": spec.ID, "description": "Makes guarded local mail, calendar, and task tools discoverable from natural-language requests.",
		"version": version, "author": map[string]string{"name": spec.Author},
		"homepage": spec.Homepage, "repository": spec.Repository, "license": spec.License,
		"keywords": spec.Keywords, "mcpServers": "./.mcp.json", "skills": "./skills/",
	}
}

func geminiManifest(spec Spec, version string) map[string]any {
	return map[string]any{
		"name": spec.ID, "version": version,
		"description": "Use the guarded local Corresync mail, calendar, and task MCP server. Requires corr on PATH; no binary, account, or credential is bundled.",
		"mcpServers": map[string]any{
			spec.ID: map[string]any{"command": spec.Launch.Command, "args": spec.Launch.Args},
		},
	}
}

func configHosts(spec Spec, version string) map[string]any {
	hosts := make([]any, 0, len(spec.ConfigOnlyHosts))
	for _, id := range spec.ConfigOnlyHosts {
		hosts = append(hosts, map[string]any{
			"id": id, "package": "config-only", "mode": "thin",
			"mcp":        map[string]any{"serverName": spec.ID, "command": spec.Launch.Command, "args": spec.Launch.Args, "transport": spec.Launch.Transport},
			"skill":      map[string]any{"name": spec.ID, "source": SkillPath},
			"limitation": "This neutral fragment is input for a host-specific lifecycle adapter; it is not a native marketplace package.",
		})
	}
	return map[string]any{
		"schemaVersion": 1, "bundle": spec.ID, "version": version,
		"requirements": spec.Requirements, "effects": spec.Effects, "hosts": hosts,
	}
}

func renderKiroPower(spec Spec, version string) []byte {
	var document strings.Builder
	fmt.Fprintf(
		&document,
		"---\nname: %s\ndisplayName: %s\ndescription: %s\n",
		spec.ID,
		strconv.Quote(spec.DisplayName),
		strconv.Quote(spec.ShortDescription),
	)
	document.WriteString("keywords:\n  - email\n  - calendar\n  - tasks\n  - mcp\n  - local-first\n")
	fmt.Fprintf(&document, "author: %s\n---\n\n# Corresync\n\nVersion: %s\n\n", strconv.Quote(spec.Author), version)
	document.WriteString("Use the local Corresync MCP server and the bundled steering guidance for guarded\n")
	document.WriteString("mail, calendar, and task work. Install Corresync separately and ensure the `corr`\n")
	document.WriteString("command is on PATH before enabling this Power. The Power contains no account,\n")
	document.WriteString("credential, token, cookie, or private configuration data. It does not support\n")
	document.WriteString("Kiro Web or a remote sandbox. Writes remain subject to Corresync's\n")
	document.WriteString("preview/commit policy, and no tool is auto-approved.\n")
	return []byte(document.String())
}

func renderKiroSteering(skill []byte) []byte {
	body := normalizeText(skill)
	if bytes.HasPrefix(body, []byte("---\n")) {
		if end := bytes.Index(body[4:], []byte("\n---\n")); end >= 0 {
			body = body[4+end+5:]
		}
	}
	return append([]byte("---\ninclusion: auto\n---\n\n"), normalizeText(body)...)
}

func renderDocumentation(spec Spec, version string) []byte {
	var document strings.Builder
	document.WriteString("<!-- Generated by go run ./tools/integrationbundle; do not edit. -->\n\n")
	document.WriteString("# Integration bundles\n\n")
	document.WriteString("Canonical source snapshot:\n\n")
	fmt.Fprintf(&document, "`%s`.\n\n", version)
	document.WriteString("Release builds render the exact tag version into every package.\n\n")
	document.WriteString("All thin packages start `corr mcp serve` over local stdio and require a\n")
	document.WriteString("separately installed Corresync CLI. They contain no accounts, credentials,\n")
	document.WriteString("mailbox data, or private configuration. No package enables automatic\n")
	document.WriteString("approval.\n\n")
	document.WriteString("<!-- markdownlint-disable MD013 -->\n")
	document.WriteString("| Surface | Native package | Mode | Agent Skill | Local MCP | Limitation |\n")
	document.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, surface := range spec.Surfaces {
		fmt.Fprintf(&document, "| `%s` | `%s` | `%s` | %s | %s | %s |\n",
			surface.ID, surface.Package, surface.Mode, yesNo(surface.Skill), yesNo(surface.LocalMCP), surface.Limitation)
	}
	document.WriteString("<!-- markdownlint-enable MD013 -->\n\n")
	document.WriteString("Config-only metadata is generated for:\n\n")
	for _, host := range spec.ConfigOnlyHosts {
		fmt.Fprintf(&document, "- `%s`\n", host)
	}
	document.WriteString("\nA lifecycle adapter must translate the neutral launch and Skill metadata into\n")
	document.WriteString("each host's documented schema; the metadata is not presented as a native\n")
	document.WriteString("package.\n")
	return []byte(document.String())
}

func renderPublicationDocumentation(spec Spec, version string) []byte {
	channels, err := spec.PublicationSnapshot(version)
	if err != nil {
		panic("validated publication version rejected: " + err.Error())
	}
	var document strings.Builder
	document.WriteString("<!-- Generated by go run ./tools/integrationbundle; do not edit. -->\n\n")
	document.WriteString("# Publication channels\n\n")
	fmt.Fprintf(&document, "Canonical source snapshot: `%s`.\n\n", version)
	document.WriteString("A native package in this repository is not the same as an upstream\n")
	document.WriteString("directory listing. Only `published` rows claim an externally visible\n")
	document.WriteString("release; `source-available` rows can be installed from this repository\n")
	document.WriteString("but are not official marketplace listings.\n\n")
	document.WriteString("<!-- markdownlint-disable MD013 -->\n")
	document.WriteString("| Channel | Package | Supported surfaces | State | Visible version | Owner | Publish method | Verification | Submission/review | Install or reload behavior | Limitation |\n")
	document.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, channel := range channels {
		visible := "—"
		if channel.ObservedVersion != "" {
			visible = "`" + channel.ObservedVersion + "`"
		}
		fmt.Fprintf(&document, "| %s | `%s` | %s | `%s` | %s | %s | %s | [check](%s) | [instructions](%s) | %s | %s |\n",
			channel.DisplayName, channel.PackageFormat, strings.Join(channel.SupportedSurfaces, ", "),
			channel.State, visible, channel.Owner, channel.PublishMethod, channel.VerificationURL,
			channel.SubmissionURL, channel.InstallBehavior, channel.Limitation)
	}
	document.WriteString("<!-- markdownlint-enable MD013 -->\n\n")
	document.WriteString("Every supported surface is local. No row enables a hosted relay, private\n")
	document.WriteString("MCP tunnel, remote sandbox, or hosted-agent path. Stable-only automation\n")
	document.WriteString("must reuse the already signed and checksummed release artifacts; preview\n")
	document.WriteString("and RC tags never publish immutable directory records.\n")
	return []byte(document.String())
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}

func validateSkill(data []byte, name string) error {
	normalized := normalizeText(data)
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return errors.New("agent skill is missing YAML frontmatter")
	}
	end := bytes.Index(normalized[4:], []byte("\n---\n"))
	if end < 0 {
		return errors.New("agent skill has unterminated YAML frontmatter")
	}
	frontmatter := string(normalized[4 : 4+end])
	fields, err := parseSkillFrontmatter(frontmatter)
	if err != nil {
		return err
	}
	if fields["name"] != name {
		return fmt.Errorf("agent skill name is %q, want %q", fields["name"], name)
	}
	if fields["description"] == "" {
		return errors.New("agent skill frontmatter has no description")
	}
	return nil
}

func parseSkillFrontmatter(frontmatter string) (map[string]string, error) {
	fields := make(map[string]string)
	for lineNumber, line := range strings.Split(frontmatter, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key != "name" && key != "description" {
			continue
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("agent skill frontmatter has duplicate %q at line %d", key, lineNumber+1)
		}
		fields[key] = strings.TrimSpace(value)
	}
	return fields, nil
}

func normalizeText(data []byte) []byte {
	return append(bytes.TrimRight(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), "\n"), '\n')
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "—"
}
