package mcpserver

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

// SignInBackend is the optional session-owner extension behind
// account_sign_in (ADR 0039). RequestAccountSignIn never starts anything;
// ResolveAccountSignIn starts a sign-in only for the approved decision, which
// this package sends only after the person accepted the server-authored
// elicitation in the MCP client.
type SignInBackend interface {
	RequestAccountSignIn(context.Context, domain.AccountID, domain.Caller) (application.AccountSignInRequest, error)
	ResolveAccountSignIn(context.Context, application.AccountSignInResolution, domain.Caller) (application.AccountSignInResult, error)
}

// AccountSignInInput is deliberately the account alias only. No model-chosen
// text can reach the approval prompt.
type AccountSignInInput struct {
	Account string `json:"account" jsonschema:"Configured account alias whose sign-in the person should be asked to approve"`
}

const (
	// signInInputRequestID names the one elicitation in a multi-round-trip
	// input_required result.
	signInInputRequestID = "corresync_account_sign_in"
	// inputRequestsProtocolVersion is the first MCP revision in which servers
	// embed elicitation in a result instead of sending elicitation/create.
	inputRequestsProtocolVersion = "2026-07-28"
	signInStateVersion           = 1
)

var signInStateAdditionalData = []byte("io.github.nkiyohara.corresync/account_sign_in/v1")

// signInStateSealer encrypts and authenticates the requestState of a
// multi-round-trip sign-in with a key that never leaves this process.
type signInStateSealer struct {
	aead cipher.AEAD
}

type signInState struct {
	Version   int              `json:"v"`
	Ticket    string           `json:"t"`
	Account   domain.AccountID `json:"a"`
	ExpiresAt time.Time        `json:"e"`
}

func newSignInStateSealer(random io.Reader) (*signInStateSealer, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, fmt.Errorf("generate sign-in state key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &signInStateSealer{aead: aead}, nil
}

func (sealer *signInStateSealer) seal(state signInState) (string, error) {
	plaintext, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, sealer.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := sealer.aead.Seal(nonce, nonce, plaintext, signInStateAdditionalData)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (sealer *signInStateSealer) open(value string, now time.Time) (signInState, error) {
	invalid := errors.New("sign-in request state is invalid or expired; nothing was started")
	if len(value) > 1024 {
		return signInState{}, invalid
	}
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(sealed) < sealer.aead.NonceSize() {
		return signInState{}, invalid
	}
	nonce, ciphertext := sealed[:sealer.aead.NonceSize()], sealed[sealer.aead.NonceSize():]
	plaintext, err := sealer.aead.Open(nil, nonce, ciphertext, signInStateAdditionalData)
	if err != nil {
		return signInState{}, invalid
	}
	var state signInState
	if err := json.Unmarshal(plaintext, &state); err != nil ||
		state.Version != signInStateVersion ||
		application.ValidateAccountSignInTicket(state.Ticket) != nil ||
		state.Account.ValidateOpaque() != nil ||
		!now.Before(state.ExpiresAt) {
		return signInState{}, invalid
	}
	return state, nil
}

func addAccountSignInTool(
	server *mcp.Server,
	backend Backend,
	caller domain.Caller,
	sealer *signInStateSealer,
) {
	nonDestructive := false
	openWorld := true
	mcp.AddTool(server, &mcp.Tool{
		Name:  application.AccountSignInTool,
		Title: "Ask the person to approve an account sign-in",
		Description: "Use only after a Corresync result says this exact account needs sign-in. Corresync itself asks the person, in this MCP client's own confirmation prompt, " +
			"whether to open the same browser sign-in as `corr auth login --account ALIAS`; nothing starts unless the person explicitly accepts. " +
			"Passwords, MFA, and consent stay in that browser and never pass through MCP. Returns immediately with started, already_signed_in, declined, unavailable, or in_progress; " +
			"after started, check account_status instead of calling again. On declined or unavailable, show the person the returned command and stop. " +
			"Logout, browser clearing, and account changes are not available here.",
		Annotations: &mcp.ToolAnnotations{
			Title:           "Request a user-approved sign-in",
			ReadOnlyHint:    false,
			DestructiveHint: &nonDestructive,
			OpenWorldHint:   &openWorld,
		},
		Meta: mcp.Meta{
			"io.github.nkiyohara.corresync/data-classification": "private-account-metadata",
			"io.github.nkiyohara.corresync/effect":              "reversible_write",
		},
	}, func(
		ctx context.Context,
		request *mcp.CallToolRequest,
		input AccountSignInInput,
	) (*mcp.CallToolResult, application.AccountSignInResult, error) {
		return accountSignIn(ctx, request, input, backend, caller, sealer)
	})
}

func accountSignIn(
	ctx context.Context,
	request *mcp.CallToolRequest,
	input AccountSignInInput,
	backend Backend,
	caller domain.Caller,
	sealer *signInStateSealer,
) (*mcp.CallToolResult, application.AccountSignInResult, error) {
	if strings.TrimSpace(input.Account) == "" {
		return nil, application.AccountSignInResult{}, errors.New("account alias is required")
	}
	accountID, err := backend.ResolveAccount(input.Account)
	if err != nil {
		return nil, application.AccountSignInResult{}, err
	}
	signer, supported := backend.(SignInBackend)
	if !supported {
		result, err := application.NewAccountSignInResult(
			application.AccountSignInUnavailable,
			application.AccountSignInReasonSessionOwnerUnsupported,
			accountID, input.Account, nil,
		)
		return nil, result, err
	}
	var params *mcp.CallToolParamsRaw
	if request != nil {
		params = request.Params
	}
	if params != nil && (params.RequestState != "" || params.InputResponses != nil) {
		return completeSignInRound(ctx, params, accountID, signer, caller, sealer)
	}

	decision, err := signer.RequestAccountSignIn(ctx, accountID, caller)
	if err != nil {
		return nil, application.AccountSignInResult{}, err
	}
	if err := decision.Validate(accountID); err != nil {
		return nil, application.AccountSignInResult{}, err
	}
	if decision.Result != nil {
		return nil, *decision.Result, nil
	}
	approval := *decision.Approval
	elicitation, err := signInElicitation(approval)
	if err != nil || !clientSupportsFormElicitation(request) {
		return resolveSignIn(ctx, signer, caller, approval.Account, approval.Ticket,
			application.AccountSignInDecisionElicitationUnsupported)
	}
	if usesInputRequests(request) {
		state, err := sealer.seal(signInState{
			Version: signInStateVersion, Ticket: approval.Ticket,
			Account: approval.Account, ExpiresAt: approval.ExpiresAt,
		})
		if err != nil {
			return resolveSignIn(ctx, signer, caller, approval.Account, approval.Ticket,
				application.AccountSignInDecisionElicitationFailed)
		}
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{signInInputRequestID: elicitation},
			RequestState:  state,
		}, application.AccountSignInResult{}, nil
	}
	answerContext, cancel := context.WithDeadline(ctx, approval.ExpiresAt)
	defer cancel()
	answer, err := request.Session.Elicit(answerContext, elicitation)
	return resolveSignIn(ctx, signer, caller, approval.Account, approval.Ticket,
		signInDecision(answer, err))
}

// completeSignInRound handles the retried call of a multi-round-trip
// request. The sealed state binds the answer to the ticket this server
// reserved for the same account.
func completeSignInRound(
	ctx context.Context,
	params *mcp.CallToolParamsRaw,
	accountID domain.AccountID,
	signer SignInBackend,
	caller domain.Caller,
	sealer *signInStateSealer,
) (*mcp.CallToolResult, application.AccountSignInResult, error) {
	state, err := sealer.open(params.RequestState, time.Now())
	if err != nil {
		return nil, application.AccountSignInResult{}, err
	}
	if state.Account != accountID {
		return nil, application.AccountSignInResult{}, errors.New(
			"sign-in request state belongs to a different account; nothing was started",
		)
	}
	var answer *mcp.ElicitResult
	if response, exists := params.InputResponses[signInInputRequestID]; exists {
		answer, _ = response.(*mcp.ElicitResult)
	}
	var answerErr error
	if answer == nil {
		answerErr = errors.New("missing elicitation response")
	}
	return resolveSignIn(ctx, signer, caller, accountID, state.Ticket, signInDecision(answer, answerErr))
}

func signInDecision(answer *mcp.ElicitResult, err error) application.AccountSignInDecision {
	if err != nil || answer == nil {
		return application.AccountSignInDecisionElicitationFailed
	}
	switch answer.Action {
	case "accept":
		return application.AccountSignInDecisionApprovedByUser
	case "decline":
		return application.AccountSignInDecisionDeclinedByUser
	case "cancel":
		return application.AccountSignInDecisionCancelledByUser
	default:
		return application.AccountSignInDecisionElicitationFailed
	}
}

func resolveSignIn(
	ctx context.Context,
	signer SignInBackend,
	caller domain.Caller,
	account domain.AccountID,
	ticket string,
	decision application.AccountSignInDecision,
) (*mcp.CallToolResult, application.AccountSignInResult, error) {
	// The person's answer must reach the session owner even if the client
	// cancelled the call while the prompt was open.
	result, err := signer.ResolveAccountSignIn(context.WithoutCancel(ctx), application.AccountSignInResolution{
		Account: account, Ticket: ticket, Decision: decision,
	}, caller)
	if err != nil {
		return nil, application.AccountSignInResult{}, err
	}
	if result.Account != account {
		return nil, application.AccountSignInResult{}, errors.New("sign-in result names a different account")
	}
	return nil, result, nil
}

func clientSupportsFormElicitation(request *mcp.CallToolRequest) bool {
	if request == nil {
		return false
	}
	capabilities := request.ClientCapabilities()
	if capabilities == nil || capabilities.Elicitation == nil {
		return false
	}
	// Both form and URL omitted means form for backward compatibility.
	return capabilities.Elicitation.Form != nil || capabilities.Elicitation.URL == nil
}

func usesInputRequests(request *mcp.CallToolRequest) bool {
	if request == nil {
		return false
	}
	if request.Params != nil {
		if version, ok := request.Params.GetMeta()[mcp.MetaKeyProtocolVersion].(string); ok {
			return version >= inputRequestsProtocolVersion
		}
	}
	if request.Session != nil {
		if initialized := request.Session.InitializeParams(); initialized != nil {
			return initialized.ProtocolVersion >= inputRequestsProtocolVersion
		}
	}
	return false
}

// signInElicitation builds the approval prompt from the configured alias,
// provider display names, and service names only. Tool arguments and provider
// responses never reach it.
func signInElicitation(approval application.AccountSignInApproval) (*mcp.ElicitParams, error) {
	if err := approval.Validate(); err != nil {
		return nil, err
	}
	if !application.AccountSignInAliasDisplayable(approval.Alias) {
		return nil, errors.New("account alias cannot be shown safely in an approval prompt")
	}
	return &mcp.ElicitParams{
		Mode:    "form",
		Message: signInMessage(approval.Alias, approval.Routes),
		RequestedSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, nil
}

func signInMessage(alias string, routes []application.AccountSignInRoute) string {
	return fmt.Sprintf(
		"Corresync sign-in request for account %q (%s).\n\n"+
			"An assistant asked Corresync to sign in to this account. If you accept, Corresync opens the same browser sign-in as "+
			"`corr auth login --account %s` on this computer: its dedicated browser window at the configured sign-in page, "+
			"or the provider's sign-in page in your browser.\n\n"+
			"Enter your password, MFA code, and consent only in that browser, never in this chat. Nothing you type there is shared with the assistant.\n\n"+
			"Accept only if you expected this request. Declining is safe; you can sign in later yourself with the command above.",
		alias, describeSignInRoutes(routes), alias,
	)
}

func describeSignInRoutes(routes []application.AccountSignInRoute) string {
	order := make([]string, 0, len(routes))
	services := make(map[string][]string, len(routes))
	for _, route := range routes {
		name := providerDisplayName(route.Provider)
		if _, seen := services[name]; !seen {
			order = append(order, name)
		}
		services[name] = append(services[name], string(route.Service))
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		parts = append(parts, name+": "+strings.Join(services[name], ", "))
	}
	return strings.Join(parts, "; ")
}

// providerDisplayName maps closed provider IDs to fixed labels. Unknown IDs
// are validated identifiers and are shown verbatim.
func providerDisplayName(provider domain.ProviderID) string {
	switch provider { //nolint:exhaustive // Other validated provider IDs are shown verbatim.
	case domain.ProviderMicrosoftOWA:
		return "Outlook Web"
	case domain.ProviderMicrosoftGraph:
		return "Microsoft Graph"
	case domain.ProviderGoogle:
		return "Google"
	case domain.ProviderJMAP:
		return "JMAP"
	case domain.ProviderIMAPSMTP:
		return "IMAP / SMTP"
	case domain.ProviderCalDAV:
		return "CalDAV"
	case domain.ProviderTodoist:
		return "Todoist"
	case domain.ProviderGoogleTasks:
		return "Google Tasks"
	case domain.ProviderTickTick:
		return "TickTick"
	case domain.ProviderID(domain.MessagingProviderMicrosoftTeams):
		return "Microsoft Teams"
	case domain.ProviderID(domain.MessagingProviderMattermost):
		return "Mattermost"
	case domain.ProviderID(domain.MessagingProviderSlack):
		return "Slack"
	default:
		return string(provider)
	}
}
