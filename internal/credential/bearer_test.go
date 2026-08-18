package credential

import (
	"context"
	"net/http"
	"testing"
)

func TestBearerAuthorizerBindsAndErasesExternalCredential(t *testing.T) {
	secret := &Secret{value: []byte("synthetic-token-value")}
	authorizer, err := NewBearerAuthorizer("https://chat.example.test", secret)
	if err != nil {
		t.Fatalf("NewBearerAuthorizer() error = %v", err)
	}
	if err := secret.Close(); err != nil {
		t.Fatalf("Secret.Close() error = %v", err)
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://chat.example.test/api/v4/users/me", nil)
	if err := authorizer.Apply(request); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer synthetic-token-value" {
		t.Fatalf("Authorization = %q", got)
	}
	crossOrigin, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://attacker.example/api/v4/users/me", nil)
	if err := authorizer.Apply(crossOrigin); err == nil {
		t.Fatal("Apply() accepted a cross-origin request")
	}
	if err := authorizer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	request.Header.Del("Authorization")
	if err := authorizer.Apply(request); err == nil {
		t.Fatal("Apply() accepted a request after Close")
	}
}

func TestBearerAuthorizerRejectsAuthorizationSyntaxAndOriginConfusion(t *testing.T) {
	for _, test := range []struct {
		origin string
		value  string
	}{
		{"http://chat.example.test", "token"},
		{"https://chat.example.test/path", "token"},
		{"https://chat.example.test", "Bearer token"},
		{"https://chat.example.test", "token\r\nX-Evil: yes"},
	} {
		secret := &Secret{value: []byte(test.value)}
		if _, err := NewBearerAuthorizer(test.origin, secret); err == nil {
			t.Fatalf("NewBearerAuthorizer(%q, %q) accepted malformed input", test.origin, test.value)
		}
	}
}
