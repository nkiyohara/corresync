package daemonapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

type signInFakeBackend struct {
	fakeBackend
	resolutions []application.AccountSignInResolution
	callers     []domain.Caller
}

func (backend *signInFakeBackend) RequestAccountSignIn(
	_ context.Context,
	account domain.AccountID,
	caller domain.Caller,
) (application.AccountSignInRequest, error) {
	backend.callers = append(backend.callers, caller)
	result, err := application.NewAccountSignInResult(
		application.AccountSignInAlreadySignedIn, "", account, "work", nil,
	)
	return application.AccountSignInRequest{Result: &result}, err
}

func (backend *signInFakeBackend) ResolveAccountSignIn(
	_ context.Context,
	resolution application.AccountSignInResolution,
	caller domain.Caller,
) (application.AccountSignInResult, error) {
	backend.resolutions = append(backend.resolutions, resolution)
	backend.callers = append(backend.callers, caller)
	return application.NewAccountSignInResult(
		application.AccountSignInDeclined, application.AccountSignInReasonUserDeclined,
		resolution.Account, "work", nil,
	)
}

func signInWireCall(t *testing.T, server *Server, token string, method Method, params string) responseEnvelope {
	t.Helper()
	body := fmt.Sprintf(
		`{"version":%d,"id":"abcdefghijklmnop","method":%q,"caller":{"surface":"mcp","instance":"process-9"},"params":%s}`,
		ProtocolVersion, method, params,
	)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+requestHost+requestPath, strings.NewReader(body))
	request.Host = requestHost
	request.Header.Set("Authorization", authorizationType+token)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	var response responseEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

func TestSignInMethodsAreClosedTypedAndOptional(t *testing.T) {
	t.Parallel()
	token := syntheticCredential("s")
	ticket := "sit1_" + strings.Repeat("C", 43)

	plain := newTestServer(t, &fakeBackend{}, token)
	if response := signInWireCall(t, plain, token, MethodSignInRequest, `{"account":"`+testAccountID+`"}`); response.Error == nil {
		t.Fatal("a session owner without the extension accepted a sign-in request")
	}

	if response := signInWireCall(t, plain, token, MethodSignInVerified, `{"account":"`+testAccountID+`"}`); response.Error == nil {
		t.Fatal("a session owner without the extension answered a verification query")
	}

	backend := &signInFakeBackend{}
	server := newTestServer(t, backend, token)
	response := signInWireCall(t, server, token, MethodSignInRequest, `{"account":"`+testAccountID+`"}`)
	if response.Error != nil {
		t.Fatalf("request error = %+v", response.Error)
	}
	var request application.AccountSignInRequest
	if err := json.Unmarshal(response.Result, &request); err != nil || request.Validate(testAccountID) != nil {
		t.Fatalf("request result = %s, %v", response.Result, err)
	}

	for _, params := range []string{
		`{"account":"` + testAccountID + `","ticket":"` + ticket + `","decision":"approved"}`,
		`{"account":"` + testAccountID + `","ticket":"forged","decision":"approved_by_user_elicitation"}`,
		`{"account":"work","ticket":"` + ticket + `","decision":"approved_by_user_elicitation"}`,
		`{"account":"` + testAccountID + `","ticket":"` + ticket + `","decision":"approved_by_user_elicitation","prompt":"x"}`,
	} {
		if response := signInWireCall(t, server, token, MethodSignInResolve, params); response.Error == nil {
			t.Fatalf("malformed resolution reached the backend: %s", params)
		}
	}
	if len(backend.resolutions) != 0 {
		t.Fatalf("resolutions = %+v", backend.resolutions)
	}
	response = signInWireCall(t, server, token, MethodSignInResolve,
		`{"account":"`+testAccountID+`","ticket":"`+ticket+`","decision":"declined_by_user"}`)
	if response.Error != nil || len(backend.resolutions) != 1 {
		t.Fatalf("resolution = %+v", response)
	}
	for _, caller := range backend.callers {
		if caller != (domain.Caller{Surface: "mcp", Instance: "process-9"}) {
			t.Fatalf("caller was not forwarded exactly: %+v", caller)
		}
	}
}

func (backend *signInFakeBackend) SignInVerified(
	_ context.Context,
	account domain.AccountID,
	caller domain.Caller,
) (SignInVerifiedResult, error) {
	backend.callers = append(backend.callers, caller)
	return SignInVerifiedResult{Account: account, Verified: true}, nil
}

func TestSignInVerificationIsOneBooleanPerAccount(t *testing.T) {
	t.Parallel()
	token := syntheticCredential("v")
	server := newTestServer(t, &signInFakeBackend{}, token)
	response := signInWireCall(t, server, token, MethodSignInVerified, `{"account":"`+testAccountID+`"}`)
	if response.Error != nil || string(response.Result) != `{"account":"`+testAccountID+`","verified":true}` {
		t.Fatalf("response = %+v %s", response.Error, response.Result)
	}
	for _, params := range []string{`{"account":"work"}`, `{"account":"` + testAccountID + `","extra":1}`} {
		if response := signInWireCall(t, server, token, MethodSignInVerified, params); response.Error == nil {
			t.Fatalf("malformed query accepted: %s", params)
		}
	}
}
