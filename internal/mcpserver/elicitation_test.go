package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

// An MCP call can never start authentication, even for a client that offers
// elicitation. The tool result names the exact local command for the human
// and leaves the retry to them.
func TestAuthenticationActionNeverElicitsAndNamesTheExactLocalLogin(t *testing.T) {
	t.Parallel()
	for _, state := range []application.AuthenticationState{
		application.AuthenticationStateSignedOut,
		application.AuthenticationStateReauthenticationNeeded,
	} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			reason := application.AuthenticationReasonSessionExpired
			if state == application.AuthenticationStateSignedOut {
				reason = application.AuthenticationReasonNeverAuthenticated
			}
			action, err := application.NewAuthenticationActionRequired(
				state, reason,
				"acc_00000000000000000000000000000001", "work",
				application.AuthenticationServiceMail, domain.ProviderMicrosoftOWA,
			)
			if err != nil {
				t.Fatal(err)
			}
			server, err := New(
				&fakeBackend{err: application.NewAuthenticationActionError(action)},
				Options{Version: "dev", Instance: "elicitation-test"},
			)
			if err != nil {
				t.Fatal(err)
			}
			var elicitations atomic.Int32
			clientTransport, serverTransport := mcp.NewInMemoryTransports()
			serverSession, err := server.Connect(t.Context(), serverTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = serverSession.Close() })
			client := mcp.NewClient(
				&mcp.Implementation{Name: "elicitation-client", Version: "dev"},
				&mcp.ClientOptions{ElicitationHandler: func(
					context.Context, *mcp.ElicitRequest,
				) (*mcp.ElicitResult, error) {
					elicitations.Add(1)
					return &mcp.ElicitResult{Action: "accept"}, nil
				}},
			)
			session, err := client.Connect(t.Context(), clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })

			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "mail_list", Arguments: map[string]any{"account": "work"},
			})
			if err != nil {
				t.Fatalf("CallTool() protocol error = %v", err)
			}
			if elicitations.Load() != 0 {
				t.Fatalf("server sent %d elicitation requests", elicitations.Load())
			}
			encoded, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var returned application.AuthenticationActionRequired
			if err := json.Unmarshal(encoded, &returned); err != nil {
				t.Fatal(err)
			}
			next := returned.NextAction
			if !result.IsError || returned.Alias != "work" ||
				next.Kind != "local_interactive_login" ||
				next.Command.Executable != "corr" ||
				!slices.Equal(next.Command.Args, []string{"auth", "login", "--account", "work"}) ||
				!next.RequiresUserConsent || !next.RequiresHumanInteraction ||
				next.SecretsAllowedInMCP || returned.Retry.Automatic ||
				!returned.Retry.AlternativeRequiresUserChoice {
				t.Fatalf("action-required result = %s (isError=%t)", encoded, result.IsError)
			}
		})
	}
}
