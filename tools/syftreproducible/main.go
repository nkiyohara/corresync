// Command syftreproducible runs Syft and gives its JSON document stable metadata.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var releaseTimestamp string

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "reproducible SBOM: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	format, document, err := outputDocument(arguments)
	if err != nil {
		return err
	}
	when, err := time.Parse(time.RFC3339, releaseTimestamp)
	if err != nil {
		return fmt.Errorf("invalid embedded release timestamp: %w", err)
	}
	syft, err := exec.LookPath("syft")
	if err != nil {
		return fmt.Errorf("find syft: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	// #nosec G204,G702 -- the fixed Syft executable receives validated release basenames.
	command := exec.CommandContext(ctx, syft, arguments...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run syft: %w", err)
	}
	if err := canonicalize(document, format, when.UTC()); err != nil {
		return err
	}
	return nil
}

func outputDocument(arguments []string) (string, string, error) {
	if len(arguments) == 0 || filepath.Base(arguments[0]) != arguments[0] {
		return "", "", errors.New("artifact must be a release basename")
	}
	for index, argument := range arguments {
		if argument != "--output" || index+1 >= len(arguments) {
			continue
		}
		format, document, found := strings.Cut(arguments[index+1], "=")
		if !found || (format != "spdx-json" && format != "cyclonedx-json") {
			return "", "", errors.New("output must be spdx-json or cyclonedx-json")
		}
		if document == "" || filepath.Base(document) != document {
			return "", "", errors.New("SBOM document must be a release basename")
		}
		return format, document, nil
	}
	return "", "", errors.New("one --output FORMAT=DOCUMENT argument is required")
}

func canonicalize(path, format string, when time.Time) error {
	data, err := readReleaseFile(path)
	if err != nil {
		return fmt.Errorf("read SBOM %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode SBOM %q: %w", path, err)
	}
	normalizeGeneratedPaths(document)
	timestamp := when.Format(time.RFC3339)
	switch format {
	case "spdx-json":
		creation, err := objectField(document, "creationInfo")
		if err != nil {
			return err
		}
		creation["created"] = timestamp
		document["documentNamespace"] = ""
	case "cyclonedx-json":
		metadata, err := objectField(document, "metadata")
		if err != nil {
			return err
		}
		metadata["timestamp"] = timestamp
		canonicalizeCycloneDXRootReference(document, metadata)
		document["serialNumber"] = ""
	default:
		return fmt.Errorf("unsupported SBOM format %q", format)
	}
	canonicalizeArrayOrder(document)

	canonical, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode canonical SBOM: %w", err)
	}
	digest := sha256.Sum256(canonical)
	if format == "spdx-json" {
		document["documentNamespace"] = "https://github.com/nkiyohara/corresync/sbom/spdx/" + hex.EncodeToString(digest[:])
	} else {
		document["serialNumber"] = deterministicUUID(digest)
	}
	canonical, err = json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode final SBOM: %w", err)
	}
	canonical = append(canonical, '\n')
	if err := writeReleaseFile(path, canonical); err != nil {
		return fmt.Errorf("write SBOM %q: %w", path, err)
	}
	return nil
}

func canonicalizeCycloneDXRootReference(document, metadata map[string]any) {
	component, ok := metadata["component"].(map[string]any)
	if !ok {
		return
	}
	oldReference, ok := component["bom-ref"].(string)
	if !ok || oldReference == "" {
		return
	}
	name, _ := component["name"].(string)
	version, _ := component["version"].(string)
	componentType, _ := component["type"].(string)
	digest := sha256.Sum256([]byte(name + "\x00" + version + "\x00" + componentType))
	replaceStringValue(document, oldReference, deterministicUUID(digest))
}

func replaceStringValue(value any, oldValue, newValue string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok && text == oldValue {
				typed[key] = newValue
				continue
			}
			replaceStringValue(child, oldValue, newValue)
		}
	case []any:
		for index, child := range typed {
			if text, ok := child.(string); ok && text == oldValue {
				typed[index] = newValue
				continue
			}
			replaceStringValue(child, oldValue, newValue)
		}
	}
}

func normalizeGeneratedPaths(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if text, ok := child.(string); ok {
				typed[key] = trimSyftArchiveTempDir(text)
				continue
			}
			normalizeGeneratedPaths(child)
		}
	case []any:
		for index, child := range typed {
			if text, ok := child.(string); ok {
				typed[index] = trimSyftArchiveTempDir(text)
				continue
			}
			normalizeGeneratedPaths(child)
		}
	}
}

func canonicalizeArrayOrder(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			canonicalizeArrayOrder(child)
		}
	case []any:
		for _, child := range typed {
			canonicalizeArrayOrder(child)
		}
		sort.SliceStable(typed, func(left, right int) bool {
			leftJSON, leftErr := json.Marshal(typed[left])
			rightJSON, rightErr := json.Marshal(typed[right])
			if leftErr != nil || rightErr != nil {
				return false
			}
			return bytes.Compare(leftJSON, rightJSON) < 0
		})
	}
}

func trimSyftArchiveTempDir(value string) string {
	const marker = "syft-archive-contents-"
	markerIndex := strings.Index(value, marker)
	if markerIndex < 0 {
		return value
	}
	remainder := value[markerIndex+len(marker):]
	separator := strings.IndexAny(remainder, `/\\`)
	if separator < 1 {
		return value
	}
	for _, character := range remainder[:separator] {
		if character < '0' || character > '9' {
			return value
		}
	}
	return filepath.ToSlash(remainder[separator+1:])
}

func objectField(document map[string]any, name string) (map[string]any, error) {
	value, ok := document[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("SBOM is missing object %q", name)
	}
	return value, nil
}

func deterministicUUID(digest [sha256.Size]byte) string {
	identifier := digest[:16]
	identifier[6] = (identifier[6] & 0x0f) | 0x80 // RFC 9562 UUIDv8.
	identifier[8] = (identifier[8] & 0x3f) | 0x80 // RFC 4122 variant.
	return fmt.Sprintf(
		"urn:uuid:%x-%x-%x-%x-%x",
		identifier[0:4],
		identifier[4:6],
		identifier[6:8],
		identifier[8:10],
		identifier[10:16],
	)
}

// The wrapper only accepts release basenames in GoReleaser's dist directory.
func readReleaseFile(path string) ([]byte, error) {
	// #nosec G304,G703 -- outputDocument constrains this to a release basename.
	return os.ReadFile(path)
}

func writeReleaseFile(path string, data []byte) error {
	// #nosec G306,G703 -- outputDocument constrains this to a public release basename.
	return os.WriteFile(path, data, 0o644)
}
