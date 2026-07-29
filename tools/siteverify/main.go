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
	siteBaseURL    = "https://nkiyohara.github.io/corresync/"
	socialImageURL = siteBaseURL + "social-card.png"
)

type page struct {
	path        string
	canonical   string
	title       string
	description string
	ids         map[string]struct{}
	links       []string
	assets      []string
}

type sitemap struct {
	URLs []struct {
		Location string `xml:"loc"`
	} `xml:"url"`
}

func main() {
	if err := verifySite("site"); err != nil {
		fmt.Fprintln(os.Stderr, "site verification failed:", err)
		os.Exit(1)
	}
	fmt.Println("static site verification passed")
}

func verifySite(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read site directory: %w", err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".html") {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return errors.New("no HTML pages found")
	}

	pages := make(map[string]page, len(paths))
	titles := make(map[string]string, len(paths))
	descriptions := make(map[string]string, len(paths))
	canonicals := make(map[string]string, len(paths))
	for _, path := range paths {
		item, err := parsePage(path)
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
		if err := verifyLocalReferences(root, item, pages); err != nil {
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
	return nil
}

func parsePage(path string) (page, error) {
	file, err := os.Open(path) // #nosec G304 -- repository-owned path selected from site/.
	if err != nil {
		return page{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	document, err := html.Parse(io.LimitReader(file, 2<<20))
	if err != nil {
		return page{}, fmt.Errorf("parse %s: %w", path, err)
	}

	item := page{path: path, ids: make(map[string]struct{})}
	meta := make(map[string][]string)
	var titles []string
	h1Count := 0
	jsonLDCount := 0
	var walk func(*html.Node) error
	walk = func(node *html.Node) error {
		if node.Type == html.ElementNode {
			attributes := attributeMap(node)
			if id := strings.TrimSpace(attributes["id"]); id != "" {
				if _, duplicate := item.ids[id]; duplicate {
					return fmt.Errorf("%s contains duplicate id %q", path, id)
				}
				item.ids[id] = struct{}{}
			}
			switch node.Data {
			case "title":
				titles = append(titles, strings.TrimSpace(textContent(node)))
			case "h1":
				h1Count++
			case "meta":
				key := attributes["name"]
				if key == "" {
					key = attributes["property"]
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
				case "stylesheet", "icon":
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
					if source != "copy.js" {
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
	item.title = titles[0]
	item.description, err = oneMeta(path, meta, "description")
	if err != nil {
		return page{}, err
	}
	expectedCanonical := siteBaseURL + filepath.Base(path)
	if filepath.Base(path) == "index.html" {
		expectedCanonical = siteBaseURL
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
	if meta["og:url"][0] != item.canonical {
		return page{}, fmt.Errorf("%s og:url does not match its canonical URL", path)
	}
	if meta["og:image"][0] != socialImageURL || meta["twitter:image"][0] != socialImageURL {
		return page{}, fmt.Errorf("%s does not use the canonical social preview image", path)
	}
	if meta["twitter:card"][0] != "summary_large_image" {
		return page{}, fmt.Errorf("%s does not request a large Twitter summary card", path)
	}
	if filepath.Base(path) == "index.html" && jsonLDCount != 1 {
		return page{}, fmt.Errorf("%s contains %d JSON-LD blocks, want 1", path, jsonLDCount)
	}
	return item, nil
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
		pathPart, fragment, _ := strings.Cut(rawReference, "#")
		targetPath := item.path
		if pathPart != "" && pathPart != "./" {
			targetPath = filepath.Join(filepath.Dir(item.path), filepath.FromSlash(pathPart))
		}
		targetPath = filepath.Clean(targetPath)
		info, err := os.Stat(targetPath)
		if err != nil {
			return fmt.Errorf("%s references missing %q: %w", item.path, rawReference, err)
		}
		if info.IsDir() {
			targetPath = filepath.Join(targetPath, "index.html")
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
		if !strings.HasPrefix(targetPath, filepath.Clean(root)+string(filepath.Separator)) {
			return fmt.Errorf("%s reference escapes the site root: %q", item.path, rawReference)
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
