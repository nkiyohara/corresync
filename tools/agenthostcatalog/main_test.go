package main

import (
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func TestRenderIncludesEveryCatalogEntryAndTruthfulLifecycle(t *testing.T) {
	t.Parallel()

	catalog := agenthost.DefaultCatalog()
	content := string(render(catalog))
	for _, host := range catalog.Hosts() {
		if count := strings.Count(content, "| ["+host.DisplayName+"]("); count != 1 {
			t.Errorf("host %q appears %d times", host.ID, count)
		}
	}
	for _, want := range []string{
		"`codex` (openai-codex)",
		"`verified` | yes | yes | plugin | — | yes | — | setup + inspect + verify + repair + remove",
		"`claude-desktop` (—) | desktop | `verified` | yes | — | — | yes | — | — | planned adapter",
		"`catalog_only` | — | — | — | — | — | — | detect only",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("rendered catalog does not contain %q", want)
		}
	}
}
