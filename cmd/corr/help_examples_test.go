package main

import (
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// helpExamples returns the command lines under "Examples:" in detail help.
func helpExamples(detail string) [][]string {
	_, examples, found := strings.Cut(detail, "Examples:\n")
	if !found {
		return nil
	}
	var commands [][]string
	for line := range strings.Lines(examples) {
		words := strings.Fields(line)
		if len(words) == 0 {
			break
		}
		commands = append(commands, words)
	}
	return commands
}

func TestHelpExamplesParse(t *testing.T) {
	t.Parallel()

	withExamples := map[string]bool{}
	var walk func(node *kong.Node)
	walk = func(node *kong.Node) {
		for _, words := range helpExamples(node.Detail) {
			if words[0] != commandName {
				t.Errorf("%s example %q does not start with %s", node.FullPath(), strings.Join(words, " "), commandName)
				continue
			}
			var commandLine cli
			parser, err := kong.New(
				&commandLine,
				kong.Name(commandName),
				kong.Exit(func(int) { t.Fatalf("parsing %q exited", strings.Join(words, " ")) }),
			)
			if err != nil {
				t.Fatalf("build command model: %v", err)
			}
			if _, err := parser.Parse(words[1:]); err != nil {
				t.Errorf("%s example %q does not parse: %v", node.FullPath(), strings.Join(words, " "), err)
			}
			withExamples[node.Name] = true
		}
		for _, child := range node.Children {
			if !child.Hidden {
				walk(child)
			}
		}
	}
	for _, command := range helpStyleModel(t).Children {
		if !command.Hidden {
			walk(command)
		}
	}
	for _, name := range []string{
		"auth", "daemon", "doctor", "events", "feedback", "import",
		"integrations", "login", "mcp", "monitor", "update",
	} {
		if !withExamples[name] {
			t.Errorf("%s --help has no examples", name)
		}
	}
}

func TestHelpExamplesFindsCommandLines(t *testing.T) {
	t.Parallel()

	got := helpExamples("Text.\n\nExamples:\n  corr auth status\n  corr auth login --account work\n\nMore text.")
	if len(got) != 2 || strings.Join(got[1], " ") != "corr auth login --account work" {
		t.Fatalf("helpExamples() = %q", got)
	}
}
