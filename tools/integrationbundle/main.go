// Command integrationbundle renders the checked-in host integration packages
// from Corresync's canonical public metadata and portable Agent Skill.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nkiyohara/corresync/internal/integrationbundle"
)

func main() {
	root := flag.String("root", ".", "repository root")
	outputRoot := flag.String("output-root", "", "output root (defaults to repository root)")
	version := flag.String("version", integrationbundle.SourceVersion(), "SemVer to render")
	check := flag.Bool("check", false, "fail if checked-in generated files differ")
	cleanOutput := flag.Bool("clean-output", false, "replace the dedicated .release/integrationbundle staging tree")
	flag.Parse()

	destination := *outputRoot
	if destination == "" {
		destination = *root
	}
	if err := run(*root, destination, *version, *check, *cleanOutput); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "integration bundle generation failed: %v\n", err)
		os.Exit(1)
	}
}

func run(root, outputRoot, version string, check, cleanOutput bool) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	absoluteOutput, err := filepath.Abs(outputRoot)
	if err != nil {
		return fmt.Errorf("resolve output root: %w", err)
	}
	if cleanOutput {
		if check {
			return errors.New("--clean-output cannot be combined with --check")
		}
		if err := cleanReleaseOutput(absoluteRoot, absoluteOutput); err != nil {
			return err
		}
	}
	skill, err := readSource(absoluteRoot, integrationbundle.SkillPath)
	if err != nil {
		return err
	}
	icon, err := readSource(absoluteRoot, integrationbundle.IconPath)
	if err != nil {
		return err
	}
	outputs, err := integrationbundle.Render(version, integrationbundle.Inputs{Skill: skill, Icon: icon})
	if err != nil {
		return err
	}
	publishedRegistry, err := readSource(absoluteRoot, "server.json")
	if err != nil {
		return err
	}
	if err := integrationbundle.ValidateRegistryManifest(publishedRegistry); err != nil {
		return fmt.Errorf("validate published server.json: %w", err)
	}
	if absoluteOutput != absoluteRoot {
		for _, relative := range []string{
			"plugins/corresync/README.md",
			integrationbundle.IconPath,
			integrationbundle.SkillPath,
			"plugins/corresync/skills/corresync/agents/openai.yaml",
		} {
			data, readErr := readSource(absoluteRoot, relative)
			if readErr != nil {
				return readErr
			}
			outputs = append(outputs, integrationbundle.Output{Path: relative, Data: data})
		}
	}

	var drift []string
	for _, output := range outputs {
		path, err := outputPath(absoluteOutput, output.Path)
		if err != nil {
			return err
		}
		if check {
			existing, readErr := os.ReadFile(path) // #nosec G304 -- path is confined to the repository root.
			if readErr != nil {
				if errors.Is(readErr, os.ErrNotExist) {
					drift = append(drift, output.Path+" (missing)")
					continue
				}
				return fmt.Errorf("read generated file %s: %w", output.Path, readErr)
			}
			if !bytes.Equal(existing, output.Data) {
				drift = append(drift, output.Path)
			}
			continue
		}
		if err := writeAtomic(path, output.Data); err != nil {
			return fmt.Errorf("write generated file %s: %w", output.Path, err)
		}
	}
	if len(drift) > 0 {
		return fmt.Errorf("generated integration assets are stale:\n  %s\nrun `mise exec -- task integrations:generate`", strings.Join(drift, "\n  "))
	}
	return nil
}

func cleanReleaseOutput(root, output string) error {
	want := filepath.Join(".release", "integrationbundle")
	relative, err := filepath.Rel(root, output)
	if err != nil || relative != want {
		return fmt.Errorf("--clean-output is restricted to %s below the repository root", filepath.ToSlash(want))
	}
	releaseRoot := filepath.Join(root, ".release")
	if info, err := os.Lstat(releaseRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New(".release must be a real directory before cleaning integration staging")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect .release: %w", err)
	}
	if info, err := os.Lstat(output); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("integration staging must be a real directory before cleaning")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect integration staging: %w", err)
	}
	if err := os.RemoveAll(output); err != nil {
		return fmt.Errorf("clean integration staging: %w", err)
	}
	return nil
}

func readSource(root, relative string) ([]byte, error) {
	path, err := outputPath(root, relative)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is confined to the repository root.
	if err != nil {
		return nil, fmt.Errorf("read canonical source %s: %w", relative, err)
	}
	return data, nil
}

func outputPath(root, relative string) (string, error) {
	native := filepath.FromSlash(relative)
	if relative == "" || filepath.IsAbs(native) || filepath.VolumeName(native) != "" ||
		strings.HasPrefix(native, string(filepath.Separator)) || filepath.Clean(native) != native ||
		strings.HasPrefix(native, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe generated path %q", relative)
	}
	path := filepath.Join(root, native)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("generated path %q escapes repository root", relative)
	}
	return path, nil
}

func writeAtomic(path string, data []byte) error {
	// #nosec G301 -- generated integration directories are public release source.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".integrationbundle-*")
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
