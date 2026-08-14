// Command publicationsite renders the localized public integration matrix
// from the same canonical publication catalog used by release documentation.
package main

import (
	"bytes"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nkiyohara/corresync/internal/integrationbundle"
	"golang.org/x/net/html"
)

const siteBaseURL = "https://corresync.org/"

//go:embed page.tmpl
var pageTemplate string

type languageLink struct {
	Language string
	Label    string
	HREF     string
	Current  bool
}

type channelRow struct {
	Name         string
	Package      string
	Surfaces     string
	State        string
	StateClass   string
	Version      string
	PublishPath  string
	Verification string
}

type pageData struct {
	copy
	Canonical       string
	AssetPrefix     string
	LanguageLinks   []languageLink
	OGAlternates    []string
	SourceVersion   string
	StableVersion   string
	RegistryVersion string
	Channels        []channelRow
}

func main() {
	root := flag.String("root", "site", "static site root")
	check := flag.Bool("check", false, "fail when generated pages are stale")
	flag.Parse()
	if err := run(*root, *check); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "publication site generation failed:", err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	tmpl, err := template.New("page").Parse(pageTemplate)
	if err != nil {
		return fmt.Errorf("parse page template: %w", err)
	}
	spec, err := integrationbundle.Load()
	if err != nil {
		return err
	}
	channels, err := spec.PublicationSnapshot(spec.SourceVersion)
	if err != nil {
		return err
	}
	for _, locale := range copies() {
		data, err := makePageData(locale, spec.SourceVersion, channels)
		if err != nil {
			return err
		}
		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, data); err != nil {
			return fmt.Errorf("render %s page: %w", locale.Language, err)
		}
		path := filepath.Join(root, locale.Prefix, "integrations.html")
		if check {
			existing, readErr := os.ReadFile(path) // #nosec G304 -- fixed generated path under the selected site root.
			if readErr != nil {
				return fmt.Errorf("read %s: %w", path, readErr)
			}
			want, normalizeErr := normalizeHTML(rendered.Bytes())
			if normalizeErr != nil {
				return fmt.Errorf("normalize generated %s: %w", path, normalizeErr)
			}
			got, normalizeErr := normalizeHTML(existing)
			if normalizeErr != nil {
				return fmt.Errorf("normalize checked-in %s: %w", path, normalizeErr)
			}
			if !bytes.Equal(got, want) {
				return fmt.Errorf("%s is stale; run `mise exec -- task integrations:generate`", path)
			}
			continue
		}
		if err := writeAtomic(path, rendered.Bytes()); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// normalizeHTML removes formatter-only whitespace while preserving the full
// token, attribute, and text contract. Checked-in Pages remain dprint-formatted
// even though localized line wrapping cannot be represented by one template.
func normalizeHTML(data []byte) ([]byte, error) {
	tokenizer := html.NewTokenizer(bytes.NewReader(data))
	var normalized strings.Builder
	for {
		switch tokenType := tokenizer.Next(); tokenType {
		case html.ErrorToken:
			if errors.Is(tokenizer.Err(), io.EOF) {
				return []byte(normalized.String()), nil
			}
			return nil, tokenizer.Err()
		case html.TextToken:
			text := strings.Join(strings.Fields(string(tokenizer.Text())), " ")
			if text != "" {
				normalized.WriteString("T")
				normalized.WriteString(text)
				normalized.WriteByte(0)
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			token := tokenizer.Token()
			sort.Slice(token.Attr, func(i, j int) bool {
				if token.Attr[i].Namespace != token.Attr[j].Namespace {
					return token.Attr[i].Namespace < token.Attr[j].Namespace
				}
				return token.Attr[i].Key < token.Attr[j].Key
			})
			normalized.WriteString("S")
			normalized.WriteString(token.Data)
			normalized.WriteByte(0)
			for _, attribute := range token.Attr {
				normalized.WriteString(attribute.Namespace)
				normalized.WriteByte(':')
				normalized.WriteString(attribute.Key)
				normalized.WriteByte('=')
				normalized.WriteString(attribute.Val)
				normalized.WriteByte(0)
			}
		case html.EndTagToken:
			normalized.WriteString("E")
			normalized.WriteString(tokenizer.Token().Data)
			normalized.WriteByte(0)
		case html.DoctypeToken:
			normalized.WriteString("D")
			normalized.WriteString(tokenizer.Token().Data)
			normalized.WriteByte(0)
		case html.CommentToken:
			normalized.WriteString("C")
			normalized.WriteString(strings.TrimSpace(string(tokenizer.Text())))
			normalized.WriteByte(0)
		}
	}
}

func makePageData(locale copy, sourceVersion string, channels []integrationbundle.PublicationChannel) (pageData, error) {
	data := pageData{
		copy:          locale,
		AssetPrefix:   "../",
		SourceVersion: sourceVersion,
		OGAlternates:  make([]string, 0, len(copies())-1),
		Channels:      make([]channelRow, 0, len(channels)),
	}
	if locale.Prefix == "" {
		data.AssetPrefix = ""
		data.Canonical = siteBaseURL + "integrations.html"
	} else {
		data.Canonical = siteBaseURL + locale.Prefix + "/integrations.html"
	}
	for _, candidate := range copies() {
		if candidate.Language != locale.Language {
			data.OGAlternates = append(data.OGAlternates, candidate.OGLocale)
		}
		href := "integrations.html"
		if candidate.Prefix != locale.Prefix {
			href = ""
			if locale.Prefix != "" {
				href = "../"
			}
			if candidate.Prefix != "" {
				href += candidate.Prefix + "/"
			}
			href += "integrations.html"
		}
		data.LanguageLinks = append(data.LanguageLinks, languageLink{
			Language: candidate.Language,
			Label:    candidate.LanguageLabel,
			HREF:     href,
			Current:  candidate.Language == locale.Language,
		})
	}
	for _, channel := range channels {
		row, err := localizedRow(locale, channel)
		if err != nil {
			return pageData{}, err
		}
		data.Channels = append(data.Channels, row)
		switch channel.ID {
		case "github-releases":
			data.StableVersion = channel.ObservedVersion
		case "mcp-registry":
			data.RegistryVersion = channel.ObservedVersion
		}
	}
	if data.StableVersion == "" || data.RegistryVersion == "" {
		return pageData{}, errors.New("publication catalog is missing the stable release or MCP Registry snapshot")
	}
	return data, nil
}

func localizedRow(locale copy, channel integrationbundle.PublicationChannel) (channelRow, error) {
	packageName, ok := locale.Packages[channel.PackageFormat]
	if !ok {
		return channelRow{}, fmt.Errorf("%s has no %s package translation", locale.Language, channel.PackageFormat)
	}
	state, ok := locale.States[channel.State]
	if !ok {
		return channelRow{}, fmt.Errorf("%s has no %s state translation", locale.Language, channel.State)
	}
	surfaces := make([]string, 0, len(channel.SupportedSurfaces))
	for _, surface := range channel.SupportedSurfaces {
		translated, found := locale.Surfaces[surface]
		if !found {
			return channelRow{}, fmt.Errorf("%s has no %s surface translation", locale.Language, surface)
		}
		surfaces = append(surfaces, translated)
	}
	version := locale.NoVersion
	if channel.ObservedVersion != "" {
		version = channel.ObservedVersion
	}
	method := locale.ManualPath
	switch {
	case channel.ID == "github-releases":
		method = locale.AutomaticReleasePath
	case channel.ID == "mcp-registry":
		method = locale.StableOnlyPath
	case channel.State == "source-available":
		method = locale.SourcePath
	}
	return channelRow{
		Name:         channel.DisplayName,
		Package:      packageName,
		Surfaces:     strings.Join(surfaces, locale.ListSeparator),
		State:        state,
		StateClass:   strings.ReplaceAll(channel.State, "-", "_"),
		Version:      version,
		PublishPath:  method,
		Verification: channel.VerificationURL,
	}, nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".publicationsite-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
