package integrationlifecycle

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/agenthost"
)

func TestRenderConfigUsesSecretFreeLifecycleLaunch(t *testing.T) {
	t.Parallel()
	data, err := RenderConfig(
		agenthost.IDKimiCode, "corresync", "/opt/corresync/bin/corr",
		[]string{"--config", "/home/person/.config/corresync/config.toml", "mcp", "serve"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var document JSONDocument
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	server := document.Servers["corresync"]
	if server.Command != "/opt/corresync/bin/corr" || server.Enabled == nil || !*server.Enabled {
		t.Fatalf("server = %+v", server)
	}
	for _, forbidden := range []string{"token", "secret", "password", "autoApprove", "yolo"} {
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(forbidden)) {
			t.Fatalf("rendered config contains %q: %s", forbidden, data)
		}
	}
}
