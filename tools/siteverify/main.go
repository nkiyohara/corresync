// Command siteverify validates the static Pages artifact as one linked,
// indexable user journey.
package main

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

const (
	siteBaseURL              = "https://corresync.org/"
	socialImageURL           = siteBaseURL + "social-card.png"
	googleSiteVerification   = "du6yQYCD4HROJoMhBnPxnbcntabW8RFRJbfZrRcVcic"
	privacyPolicyRelativeURL = "privacy.html"
	termsOfUseRelativeURL    = "terms.html"
	maximumPageBytes         = 2 << 20
)

var pageFiles = []string{
	"index.html",
	"getting-started.html",
	"providers.html",
	"features.html",
	"safety.html",
	"privacy.html",
	"terms.html",
}

type page struct {
	path        string
	lang        string
	canonical   string
	title       string
	description string
	ids         map[string]struct{}
	links       []string
	assets      []string
	alternates  map[string]string
	languageNav map[string]string
	navCurrent  string
}

type sitemap struct {
	URLs []struct {
		Location string `xml:"loc"`
		Links    []struct {
			Relation string `xml:"rel,attr"`
			Language string `xml:"hreflang,attr"`
			HREF     string `xml:"href,attr"`
		} `xml:"http://www.w3.org/1999/xhtml link"`
	} `xml:"url"`
}

type localeSpec struct {
	Language string
	OGLocale string
	Prefix   string
}

var locales = []localeSpec{
	{Language: "en", OGLocale: "en_GB"},
	{Language: "ja", OGLocale: "ja_JP", Prefix: "ja"},
	{Language: "zh-Hans", OGLocale: "zh_CN", Prefix: "zh-cn"},
	{Language: "zh-Hant", OGLocale: "zh_TW", Prefix: "zh-tw"},
	{Language: "ko", OGLocale: "ko_KR", Prefix: "ko"},
}

type webManifest struct {
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	StartURL  string `json:"start_url"`
	Scope     string `json:"scope"`
	Display   string `json:"display"`
	Icons     []struct {
		Source string `json:"src"`
		Sizes  string `json:"sizes"`
		Type   string `json:"type"`
	} `json:"icons"`
}

func main() {
	if err := verifySite("site"); err != nil {
		fmt.Fprintln(os.Stderr, "site verification failed:", err)
		os.Exit(1)
	}
	fmt.Println("static site verification passed")
}

func verifySite(root string) error {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("site artifact contains symlink %s", path)
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".html") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk site directory: %w", err)
	}
	sort.Strings(paths)
	if err := verifyPageSet(root, paths); err != nil {
		return err
	}

	pages := make(map[string]page, len(paths))
	titles := make(map[string]string, len(paths))
	descriptions := make(map[string]string, len(paths))
	canonicals := make(map[string]string, len(paths))
	for _, path := range paths {
		item, err := parsePage(root, path)
		if err != nil {
			return err
		}
		if previous := titles[item.title]; previous != "" {
			return fmt.Errorf("%s and %s have the same title %q", previous, path, item.title)
		}
		if previous := descriptions[item.description]; previous != "" {
			return fmt.Errorf(
				"%s and %s have the same meta description %q",
				previous,
				path,
				item.description,
			)
		}
		if previous := canonicals[item.canonical]; previous != "" {
			return fmt.Errorf(
				"%s and %s have the same canonical URL %q",
				previous,
				path,
				item.canonical,
			)
		}
		titles[item.title] = path
		descriptions[item.description] = path
		canonicals[item.canonical] = path
		pages[filepath.Clean(path)] = item
	}

	for _, item := range pages {
		for language, alternate := range item.alternates {
			if _, ok := canonicals[alternate]; !ok {
				return fmt.Errorf(
					"%s %s alternate points to missing canonical URL %q",
					item.path,
					language,
					alternate,
				)
			}
		}
		if !containsString(item.links, privacyPolicyRelativeURL) {
			return fmt.Errorf("%s does not link the public Privacy Policy", item.path)
		}
		if !containsString(item.links, termsOfUseRelativeURL) {
			return fmt.Errorf("%s does not link the public Terms of Use", item.path)
		}
		if err := verifyLocalReferences(root, item, pages); err != nil {
			return err
		}
		if err := verifyLanguageNavigation(root, item, pages); err != nil {
			return err
		}
	}
	if err := verifySitemap(root, canonicals); err != nil {
		return err
	}
	robots, err := os.ReadFile(filepath.Join(root, "robots.txt")) // #nosec G304 -- repository-owned site root.
	if err != nil {
		return fmt.Errorf("read robots.txt: %w", err)
	}
	if !strings.Contains(string(robots), "Sitemap: "+siteBaseURL+"sitemap.xml") {
		return errors.New("robots.txt does not name the canonical sitemap")
	}
	if err := verifyWebManifest(root); err != nil {
		return err
	}
	if err := verifyCompatibilityChecker(root, pages); err != nil {
		return err
	}
	return nil
}

func verifyPageSet(root string, actual []string) error {
	expected := make(map[string]struct{}, len(locales)*len(pageFiles))
	for _, locale := range locales {
		for _, name := range pageFiles {
			expected[filepath.Join(root, locale.Prefix, name)] = struct{}{}
		}
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("site has %d HTML pages, want %d", len(actual), len(expected))
	}
	for _, path := range actual {
		if _, ok := expected[filepath.Clean(path)]; !ok {
			return fmt.Errorf("site contains unexpected HTML page %s", path)
		}
		delete(expected, filepath.Clean(path))
	}
	if len(expected) != 0 {
		missing := make([]string, 0, len(expected))
		for path := range expected {
			missing = append(missing, path)
		}
		sort.Strings(missing)
		return fmt.Errorf("site is missing HTML pages: %s", strings.Join(missing, ", "))
	}
	return nil
}

func parsePage(root, path string) (page, error) {
	file, err := os.Open(path) // #nosec G304 -- repository-owned path selected from site/.
	if err != nil {
		return page{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return page{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() > maximumPageBytes {
		return page{}, fmt.Errorf("%s is %d bytes, limit is %d", path, info.Size(), maximumPageBytes)
	}
	document, err := html.Parse(io.LimitReader(file, maximumPageBytes))
	if err != nil {
		return page{}, fmt.Errorf("parse %s: %w", path, err)
	}

	item := page{
		path:        path,
		ids:         make(map[string]struct{}),
		alternates:  make(map[string]string),
		languageNav: make(map[string]string),
	}
	meta := make(map[string][]string)
	var titles []string
	h1Count := 0
	jsonLDCount := 0
	siteHeaderCount := 0
	mainCount := 0
	siteFooterCount := 0
	languageNavCount := 0
	var walk func(*html.Node) error
	walk = func(node *html.Node) error {
		if node.Type == html.ElementNode {
			if duplicate := duplicateAttribute(node); duplicate != "" {
				return fmt.Errorf("%s contains duplicate %s attribute", path, duplicate)
			}
			attributes := attributeMap(node)
			if node.Data == "html" {
				if item.lang != "" {
					return fmt.Errorf("%s contains more than one html element", path)
				}
				item.lang = strings.TrimSpace(attributes["lang"])
			}
			if id := strings.TrimSpace(attributes["id"]); id != "" {
				if _, duplicate := item.ids[id]; duplicate {
					return fmt.Errorf("%s contains duplicate id %q", path, id)
				}
				item.ids[id] = struct{}{}
			}
			if node.Data == "header" && hasClass(attributes["class"], "site-header") {
				siteHeaderCount++
			}
			if node.Data == "main" && attributes["id"] == "main" {
				mainCount++
			}
			if node.Data == "footer" && hasClass(attributes["class"], "site-footer") {
				siteFooterCount++
			}
			if node.Data == "nav" && hasClass(attributes["class"], "language-nav") {
				languageNavCount++
				current, err := collectLanguageNavigation(path, node, item.languageNav)
				if err != nil {
					return err
				}
				item.navCurrent = current
			}
			switch node.Data {
			case "title":
				titles = append(titles, strings.TrimSpace(textContent(node)))
			case "h1":
				h1Count++
			case "meta":
				name := strings.TrimSpace(attributes["name"])
				property := strings.TrimSpace(attributes["property"])
				if name != "" && property != "" {
					return fmt.Errorf("%s contains meta with both name %q and property %q", path, name, property)
				}
				key := name
				if key == "" {
					key = property
				}
				if key != "" {
					meta[key] = append(meta[key], attributes["content"])
				}
			case "link":
				switch attributes["rel"] {
				case "canonical":
					if item.canonical != "" {
						return fmt.Errorf("%s contains more than one canonical link", path)
					}
					item.canonical = attributes["href"]
				case "alternate":
					language := strings.TrimSpace(attributes["hreflang"])
					if language == "" || strings.TrimSpace(attributes["href"]) == "" {
						return fmt.Errorf("%s contains an incomplete language alternate", path)
					}
					if _, duplicate := item.alternates[language]; duplicate {
						return fmt.Errorf("%s contains duplicate %s alternate", path, language)
					}
					item.alternates[language] = attributes["href"]
				case "stylesheet", "icon", "apple-touch-icon", "manifest":
					item.assets = append(item.assets, attributes["href"])
				}
			case "a":
				item.links = append(item.links, attributes["href"])
			case "img":
				item.assets = append(item.assets, attributes["src"])
				if _, ok := attributes["alt"]; !ok {
					return fmt.Errorf("%s contains an image without alt text", path)
				}
			case "script":
				if source := attributes["src"]; source != "" {
					base := filepath.Base(source)
					resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(source)))
					expected := filepath.Join(filepath.Clean(root), base)
					allowed := resolved == expected && (base == "copy.js" ||
						(filepath.Base(path) == "providers.html" && base == "check.js"))
					if !allowed {
						return fmt.Errorf("%s loads unexpected script %q", path, source)
					}
					item.assets = append(item.assets, source)
				} else if attributes["type"] == "application/ld+json" {
					jsonLDCount++
					payload := strings.TrimSpace(textContent(node))
					if payload == "" || !json.Valid([]byte(payload)) {
						return fmt.Errorf("%s contains invalid JSON-LD", path)
					}
				} else {
					return fmt.Errorf("%s contains an unexpected inline script", path)
				}
			}
			for name := range attributes {
				if strings.HasPrefix(strings.ToLower(name), "on") {
					return fmt.Errorf("%s contains inline event handler %q", path, name)
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(document); err != nil {
		return page{}, err
	}

	if len(titles) != 1 || titles[0] == "" {
		return page{}, fmt.Errorf("%s must contain exactly one non-empty title", path)
	}
	if h1Count != 1 {
		return page{}, fmt.Errorf("%s contains %d h1 elements, want 1", path, h1Count)
	}
	if siteHeaderCount != 1 || mainCount != 1 || siteFooterCount != 1 || languageNavCount != 1 {
		return page{}, fmt.Errorf(
			"%s layout counts: header=%d main=%d footer=%d language-nav=%d, want 1 each",
			path,
			siteHeaderCount,
			mainCount,
			siteFooterCount,
			languageNavCount,
		)
	}
	item.title = titles[0]
	locale, err := localeForPath(root, path)
	if err != nil {
		return page{}, err
	}
	if item.lang != locale.Language {
		return page{}, fmt.Errorf("%s html lang = %q, want %q", path, item.lang, locale.Language)
	}
	if item.navCurrent != locale.Language {
		return page{}, fmt.Errorf(
			"%s language navigation marks %q current, want %q",
			path,
			item.navCurrent,
			locale.Language,
		)
	}
	item.description, err = oneMeta(path, meta, "description")
	if err != nil {
		return page{}, err
	}
	expectedCanonical, err := canonicalForPath(root, path)
	if err != nil {
		return page{}, err
	}
	if item.canonical != expectedCanonical {
		return page{}, fmt.Errorf(
			"%s canonical = %q, want %q",
			path,
			item.canonical,
			expectedCanonical,
		)
	}
	for _, key := range []string{
		"og:locale",
		"og:title",
		"og:description",
		"og:type",
		"og:url",
		"og:image",
		"og:image:width",
		"og:image:height",
		"og:image:alt",
		"twitter:card",
		"twitter:title",
		"twitter:description",
		"twitter:image",
		"twitter:image:alt",
	} {
		if _, err := oneMeta(path, meta, key); err != nil {
			return page{}, err
		}
	}
	if meta["og:locale"][0] != locale.OGLocale {
		return page{}, fmt.Errorf(
			"%s og:locale = %q, want %q",
			path,
			meta["og:locale"][0],
			locale.OGLocale,
		)
	}
	if err := verifyOGAlternates(path, locale, meta["og:locale:alternate"]); err != nil {
		return page{}, err
	}
	if err := verifyLanguageAlternates(root, path, item.alternates); err != nil {
		return page{}, err
	}
	if meta["og:url"][0] != item.canonical {
		return page{}, fmt.Errorf("%s og:url does not match its canonical URL", path)
	}
	if meta["og:image"][0] != socialImageURL || meta["twitter:image"][0] != socialImageURL {
		return page{}, fmt.Errorf("%s does not use the canonical social preview image", path)
	}
	if meta["twitter:card"][0] != "summary_large_image" {
		return page{}, fmt.Errorf("%s does not request a large Twitter summary card", path)
	}
	for _, requiredAsset := range []string{
		"favicon.ico",
		"favicon.svg",
		"favicon-32x32.png",
		"apple-touch-icon.png",
		"site.webmanifest",
	} {
		if !containsRootAsset(root, item.path, item.assets, requiredAsset) {
			return page{}, fmt.Errorf("%s does not reference %s", path, requiredAsset)
		}
	}
	if filepath.Base(path) == "index.html" {
		if jsonLDCount != 1 {
			return page{}, fmt.Errorf("%s contains %d JSON-LD blocks, want 1", path, jsonLDCount)
		}
	}
	if filepath.Clean(path) == filepath.Join(filepath.Clean(root), "index.html") {
		verification, err := oneMeta(path, meta, "google-site-verification")
		if err != nil {
			return page{}, err
		}
		if verification != googleSiteVerification {
			return page{}, fmt.Errorf("%s has an unexpected Google site verification token", path)
		}
	}
	return item, nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func collectLanguageNavigation(path string, navigation *html.Node, links map[string]string) (string, error) {
	validLanguages := make(map[string]struct{}, len(locales))
	for _, locale := range locales {
		validLanguages[locale.Language] = struct{}{}
	}
	current := ""
	var walk func(*html.Node) error
	walk = func(node *html.Node) error {
		if node.Type == html.ElementNode && node.Data == "a" {
			attributes := attributeMap(node)
			language := strings.TrimSpace(attributes["hreflang"])
			href := strings.TrimSpace(attributes["href"])
			if _, ok := validLanguages[language]; !ok || href == "" {
				return fmt.Errorf("%s language navigation contains incomplete or unknown link", path)
			}
			if strings.TrimSpace(attributes["lang"]) != language {
				return fmt.Errorf("%s language navigation %s link has mismatched lang", path, language)
			}
			if _, duplicate := links[language]; duplicate {
				return fmt.Errorf("%s language navigation repeats %s", path, language)
			}
			links[language] = href
			if attributes["aria-current"] != "" {
				if attributes["aria-current"] != "page" || current != "" {
					return fmt.Errorf("%s language navigation has invalid current page", path)
				}
				current = language
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(navigation); err != nil {
		return "", err
	}
	if len(links) != len(locales) || current == "" {
		return "", fmt.Errorf(
			"%s language navigation has %d links and current %q, want %d links and one current page",
			path,
			len(links),
			current,
			len(locales),
		)
	}
	return current, nil
}

func containsRootAsset(root, pagePath string, values []string, expected string) bool {
	expectedPath := filepath.Join(filepath.Clean(root), expected)
	for _, value := range values {
		resolved := filepath.Clean(filepath.Join(filepath.Dir(pagePath), filepath.FromSlash(value)))
		if resolved == expectedPath {
			return true
		}
	}
	return false
}

func localeForPath(root, path string) (localeSpec, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return localeSpec{}, fmt.Errorf("resolve locale for %s: %w", path, err)
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) < 1 || len(parts) > 2 {
		return localeSpec{}, fmt.Errorf("%s is outside the supported page layout", path)
	}
	prefix := ""
	if len(parts) > 1 {
		prefix = parts[0]
	}
	for _, locale := range locales {
		if locale.Prefix == prefix {
			return locale, nil
		}
	}
	return localeSpec{}, fmt.Errorf("%s is in unsupported locale directory %q", path, prefix)
}

func canonicalForPath(root, path string) (string, error) {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("resolve canonical for %s: %w", path, err)
	}
	relative = filepath.ToSlash(relative)
	if relative == "index.html" {
		return siteBaseURL, nil
	}
	if strings.HasSuffix(relative, "/index.html") {
		return siteBaseURL + strings.TrimSuffix(relative, "index.html"), nil
	}
	return siteBaseURL + relative, nil
}

func verifyOGAlternates(path string, current localeSpec, actual []string) error {
	expected := make(map[string]struct{}, len(locales)-1)
	for _, locale := range locales {
		if locale.Language != current.Language {
			expected[locale.OGLocale] = struct{}{}
		}
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("%s has %d og:locale:alternate values, want %d", path, len(actual), len(expected))
	}
	for _, value := range actual {
		if _, ok := expected[value]; !ok {
			return fmt.Errorf("%s contains unexpected og:locale:alternate %q", path, value)
		}
		delete(expected, value)
	}
	return nil
}

func verifyLanguageAlternates(root, path string, actual map[string]string) error {
	expected := make(map[string]string, len(locales)+1)
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("resolve alternates for %s: %w", path, err)
	}
	leaf := filepath.Base(relative)
	if leaf == "index.html" {
		leaf = ""
	}
	for _, locale := range locales {
		localizedPath := leaf
		if locale.Prefix != "" {
			localizedPath = locale.Prefix + "/" + leaf
		}
		expected[locale.Language] = siteBaseURL + localizedPath
	}
	expected["x-default"] = expected["en"]
	if len(actual) != len(expected) {
		return fmt.Errorf("%s has %d language alternates, want %d", path, len(actual), len(expected))
	}
	for language, href := range expected {
		if actual[language] != href {
			return fmt.Errorf("%s %s alternate = %q, want %q", path, language, actual[language], href)
		}
	}
	return nil
}

func verifyLocalReferences(root string, item page, pages map[string]page) error {
	for _, rawReference := range append(item.links, item.assets...) {
		if rawReference == "" {
			return fmt.Errorf("%s contains an empty local reference", item.path)
		}
		if strings.HasPrefix(rawReference, "https://") ||
			strings.HasPrefix(rawReference, "mailto:") {
			continue
		}
		if strings.HasPrefix(rawReference, "//") {
			return fmt.Errorf("%s contains scheme-relative reference %q", item.path, rawReference)
		}
		pathPart, fragment, _ := strings.Cut(rawReference, "#")
		targetPath, err := resolveLocalPath(root, item.path, pathPart)
		if err != nil {
			return fmt.Errorf("%s reference %q: %w", item.path, rawReference, err)
		}
		info, err := os.Stat(targetPath)
		if err != nil {
			return fmt.Errorf("%s references missing %q: %w", item.path, rawReference, err)
		}
		if info.IsDir() {
			targetPath = filepath.Join(targetPath, "index.html")
			info, err = os.Stat(targetPath)
			if err != nil {
				return fmt.Errorf("%s references missing %q: %w", item.path, rawReference, err)
			}
			if info.IsDir() {
				return fmt.Errorf("%s reference %q resolves to a directory", item.path, rawReference)
			}
		}
		if !withinRoot(root, targetPath) {
			return fmt.Errorf("%s reference escapes the site root: %q", item.path, rawReference)
		}
		if fragment != "" {
			target, ok := pages[targetPath]
			if !ok {
				return fmt.Errorf(
					"%s references fragment %q on non-HTML target %s",
					item.path,
					fragment,
					targetPath,
				)
			}
			if _, ok := target.ids[fragment]; !ok {
				return fmt.Errorf(
					"%s references missing fragment %q in %s",
					item.path,
					fragment,
					targetPath,
				)
			}
		}
	}
	return nil
}

func resolveLocalPath(root, sourcePath, rawPath string) (string, error) {
	pathPart, _, _ := strings.Cut(rawPath, "?")
	if strings.ContainsAny(pathPart, "%\\") {
		return "", errors.New("encoded or backslash paths are not allowed")
	}
	targetPath := sourcePath
	if pathPart != "" {
		if strings.HasPrefix(pathPart, "/") {
			targetPath = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(pathPart, "/")))
		} else {
			targetPath = filepath.Join(filepath.Dir(sourcePath), filepath.FromSlash(pathPart))
		}
	}
	targetPath = filepath.Clean(targetPath)
	if !withinRoot(root, targetPath) {
		return "", errors.New("path escapes the site root")
	}
	return targetPath, nil
}

func withinRoot(root, target string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func verifyLanguageNavigation(root string, item page, pages map[string]page) error {
	for _, locale := range locales {
		rawReference, ok := item.languageNav[locale.Language]
		if !ok {
			return fmt.Errorf("%s language navigation is missing %s", item.path, locale.Language)
		}
		if strings.ContainsAny(rawReference, "?#") || strings.HasPrefix(rawReference, "https://") ||
			strings.HasPrefix(rawReference, "mailto:") || strings.HasPrefix(rawReference, "//") {
			return fmt.Errorf(
				"%s language navigation %s is not a direct local page link: %q",
				item.path,
				locale.Language,
				rawReference,
			)
		}
		targetPath, err := resolveLocalPath(root, item.path, rawReference)
		if err != nil {
			return fmt.Errorf("%s language navigation %s: %w", item.path, locale.Language, err)
		}
		info, err := os.Stat(targetPath)
		if err != nil {
			return fmt.Errorf("%s language navigation %s: %w", item.path, locale.Language, err)
		}
		if info.IsDir() {
			targetPath = filepath.Join(targetPath, "index.html")
		}
		target, ok := pages[filepath.Clean(targetPath)]
		if !ok {
			return fmt.Errorf(
				"%s language navigation %s points outside the HTML page set: %q",
				item.path,
				locale.Language,
				rawReference,
			)
		}
		if target.canonical != item.alternates[locale.Language] {
			return fmt.Errorf(
				"%s language navigation %s points to %q, want %q",
				item.path,
				locale.Language,
				target.canonical,
				item.alternates[locale.Language],
			)
		}
	}
	return nil
}

func verifyCompatibilityChecker(root string, pages map[string]page) error {
	for _, locale := range locales {
		providersPath := filepath.Join(root, locale.Prefix, "providers.html")
		providers, ok := pages[filepath.Clean(providersPath)]
		if !ok {
			return fmt.Errorf("site does not contain %s", providersPath)
		}
		for _, id := range []string{
			"check",
			"compatibility-form",
			"compatibility-email",
			"compatibility-submit",
			"compatibility-live",
			"compatibility-result",
		} {
			if _, ok := providers.ids[id]; !ok {
				return fmt.Errorf("%s compatibility checker is missing id %q", providersPath, id)
			}
		}
		if !containsRootAsset(root, providers.path, providers.assets, "check.js") {
			return fmt.Errorf("%s does not load the compatibility checker", providersPath)
		}
	}

	path := filepath.Join(root, "check.js")
	data, err := os.ReadFile(path) // #nosec G304 -- repository-owned site root.
	if err != nil {
		return fmt.Errorf("read compatibility checker: %w", err)
	}
	source := string(data)
	if !strings.Contains(source, "https://discover.corresync.org/v1/check") ||
		!strings.Contains(source, `JSON.stringify({ domain: normalized })`) {
		return errors.New("compatibility checker does not use the fixed domain-only contract")
	}
	for _, forbidden := range []string{
		"localStorage",
		"sessionStorage",
		"document.cookie",
		"indexedDB",
		"sendBeacon",
		"location.search",
		"location.hash",
		"innerHTML",
		"outerHTML",
	} {
		if strings.Contains(source, forbidden) {
			return fmt.Errorf("compatibility checker contains forbidden browser API %q", forbidden)
		}
	}
	return nil
}

func verifyWebManifest(root string) error {
	path := filepath.Join(root, "site.webmanifest")
	data, err := os.ReadFile(path) // #nosec G304 -- repository-owned site root.
	if err != nil {
		return fmt.Errorf("read site.webmanifest: %w", err)
	}
	var manifest webManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("parse site.webmanifest: %w", err)
	}
	if manifest.Name != "Corresync" ||
		manifest.ShortName != "Corresync" ||
		manifest.StartURL != "./" ||
		manifest.Scope != "./" ||
		manifest.Display != "standalone" {
		return errors.New("site.webmanifest has unexpected application metadata")
	}
	expectedIcons := map[string]string{
		"icon-192.png": "192x192",
		"icon-512.png": "512x512",
	}
	if len(manifest.Icons) != len(expectedIcons) {
		return fmt.Errorf(
			"site.webmanifest has %d icons, want %d",
			len(manifest.Icons),
			len(expectedIcons),
		)
	}
	for _, icon := range manifest.Icons {
		expectedSize, ok := expectedIcons[icon.Source]
		if !ok || icon.Sizes != expectedSize || icon.Type != "image/png" {
			return fmt.Errorf("site.webmanifest contains unexpected icon %#v", icon)
		}
		info, err := os.Stat(filepath.Join(root, icon.Source))
		if err != nil {
			return fmt.Errorf("site.webmanifest icon %q: %w", icon.Source, err)
		}
		if info.IsDir() || info.Size() == 0 {
			return fmt.Errorf("site.webmanifest icon %q is empty", icon.Source)
		}
	}
	return nil
}

func verifySitemap(root string, canonicals map[string]string) error {
	data, err := os.ReadFile(filepath.Join(root, "sitemap.xml")) // #nosec G304 -- repository-owned site root.
	if err != nil {
		return fmt.Errorf("read sitemap.xml: %w", err)
	}
	var document sitemap
	if err := xml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse sitemap.xml: %w", err)
	}
	locations := make(map[string]struct{}, len(document.URLs))
	for _, item := range document.URLs {
		location := strings.TrimSpace(item.Location)
		if !strings.HasPrefix(location, siteBaseURL) {
			return fmt.Errorf("sitemap contains non-canonical URL %q", location)
		}
		if _, duplicate := locations[location]; duplicate {
			return fmt.Errorf("sitemap contains duplicate URL %q", location)
		}
		locations[location] = struct{}{}
		alternateLinks := make(map[string]string, len(item.Links))
		for _, link := range item.Links {
			if link.Relation != "alternate" || link.Language == "" || link.HREF == "" {
				return fmt.Errorf("sitemap URL %q contains an incomplete language alternate", location)
			}
			if _, duplicate := alternateLinks[link.Language]; duplicate {
				return fmt.Errorf("sitemap URL %q repeats %s", location, link.Language)
			}
			alternateLinks[link.Language] = link.HREF
		}
		path, ok := canonicals[location]
		if !ok {
			return fmt.Errorf("sitemap contains unknown canonical URL %q", location)
		}
		if err := verifyLanguageAlternates(root, path, alternateLinks); err != nil {
			return fmt.Errorf("sitemap entry for %s: %w", location, err)
		}
	}
	if len(locations) != len(canonicals) {
		return fmt.Errorf(
			"sitemap has %d URLs, but the site has %d canonical HTML pages",
			len(locations),
			len(canonicals),
		)
	}
	for canonical := range canonicals {
		if _, ok := locations[canonical]; !ok {
			return fmt.Errorf("sitemap is missing canonical URL %q", canonical)
		}
	}
	return nil
}

func oneMeta(path string, meta map[string][]string, key string) (string, error) {
	values := meta[key]
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", fmt.Errorf("%s must contain exactly one non-empty %s meta value", path, key)
	}
	return strings.TrimSpace(values[0]), nil
}

func attributeMap(node *html.Node) map[string]string {
	result := make(map[string]string, len(node.Attr))
	for _, attribute := range node.Attr {
		result[attribute.Key] = attribute.Val
	}
	return result
}

func duplicateAttribute(node *html.Node) string {
	seen := make(map[string]struct{}, len(node.Attr))
	for _, attribute := range node.Attr {
		key := attribute.Namespace + "\x00" + attribute.Key
		if _, ok := seen[key]; ok {
			return attribute.Key
		}
		seen[key] = struct{}{}
	}
	return ""
}

func hasClass(value, expected string) bool {
	for _, className := range strings.Fields(value) {
		if className == expected {
			return true
		}
	}
	return false
}

func textContent(node *html.Node) string {
	var builder strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			builder.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}
