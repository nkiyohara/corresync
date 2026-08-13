package integrationlifecycle

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func TestRenderConfigUsesSecretFreeLifecycleLaunch(t *testing.T) {
	t.Parallel()
	executable := testAbsolutePath("bin", "corr")
	configPath := testAbsolutePath("config", "config.toml")
	data, err := RenderConfig(
		agenthost.IDKimiCode, "corresync", executable,
		[]string{"--config", configPath, "mcp", "serve"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var document JSONDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	server := document.Servers["corresync"]
	if server.Command != executable || server.Enabled == nil || !*server.Enabled {
		t.Fatalf("server = %+v", server)
	}
	for _, forbidden := range []string{"token", "secret", "password", "autoApprove", "yolo"} {
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(forbidden)) {
			t.Fatalf("rendered config contains %q: %s", forbidden, data)
		}
	}
}
