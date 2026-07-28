package daemonapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

// Client calls one daemon namespace and reloads its rotating credential for
// every operation. It never retries an ambiguous application call.
type Client struct {
	endpoint localipc.Endpoint
	host     string
	http     *http.Client
}

// NewClient creates a no-TCP HTTP transport over Unix socket or named pipe.
func NewClient(endpoint localipc.Endpoint) (*Client, error) {
	return newClient(endpoint, requestHost)
}

// NewLegacyClient creates the narrowly scoped v0.6 client used only to stop an
// old session owner before migrating browser profiles.
func NewLegacyClient(endpoint localipc.Endpoint) (*Client, error) {
	return newClient(endpoint, "owa.local")
}

func newClient(endpoint localipc.Endpoint, host string) (*Client, error) {
	if endpoint.Address == "" || endpoint.CredentialPath == "" {
		return nil, errors.New("complete daemon endpoint is required")
	}
	transport := &http.Transport{
		Proxy:              nil,
		DialContext:        func(ctx context.Context, _, _ string) (net.Conn, error) { return localipc.DialContext(ctx, endpoint) },
		DisableCompression: true,
		DisableKeepAlives:  true,
		MaxConnsPerHost:    maxConcurrentCalls,
	}
	return &Client{endpoint: endpoint, host: host, http: &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}, nil
}

// Close releases idle transport resources and no daemon state.
func (client *Client) Close() error {
	client.http.CloseIdleConnections()
	return nil
}

// Login asks the session owner to ensure an interactive account session.
func (client *Client) Login(ctx context.Context, account domain.AccountID, caller domain.Caller) (LoginResult, error) {
	if err := account.Validate(); err != nil {
		return LoginResult{}, err
	}
	var result LoginResult
	if err := client.call(ctx, MethodLogin, caller, LoginInput{Account: account}, &result); err != nil {
		return LoginResult{}, err
	}
	if result.Account != account || !result.Authenticated || result.CapturedAt.IsZero() {
		return LoginResult{}, errors.New("daemon returned invalid login state")
	}
	return result, nil
}

// SessionStatus returns content-free in-memory authentication state.
func (client *Client) SessionStatus(ctx context.Context, caller domain.Caller) (SessionStatusResult, error) {
	var result SessionStatusResult
	if err := client.call(ctx, MethodSessionStatus, caller, struct{}{}, &result); err != nil {
		return SessionStatusResult{}, err
	}
	if err := validateSessionStatusResult(result); err != nil {
		return SessionStatusResult{}, err
	}
	return result, nil
}

// MonitorStatus reads one account's consent and local queue health.
func (client *Client) MonitorStatus(
	ctx context.Context,
	account domain.AccountID,
	caller domain.Caller,
) (application.MonitorStatus, error) {
	if err := account.ValidateOpaque(); err != nil {
		return application.MonitorStatus{}, err
	}
	var result application.MonitorStatus
	if err := client.call(
		ctx,
		MethodMonitorStatus,
		caller,
		MonitorStatusInput{Account: account},
		&result,
	); err != nil {
		return application.MonitorStatus{}, err
	}
	if err := result.Validate(account); err != nil {
		return application.MonitorStatus{}, fmt.Errorf(
			"daemon returned invalid monitor status: %w",
			err,
		)
	}
	return result, nil
}

// ListMonitorEvents returns one bounded account-local queue page.
func (client *Client) ListMonitorEvents(
	ctx context.Context,
	input application.MonitorEventListInput,
	caller domain.Caller,
) (application.MonitorEventPage, error) {
	if err := input.Validate(); err != nil {
		return application.MonitorEventPage{}, err
	}
	var result application.MonitorEventPage
	if err := client.call(ctx, MethodEventsList, caller, input, &result); err != nil {
		return application.MonitorEventPage{}, err
	}
	if err := result.Validate(input); err != nil {
		return application.MonitorEventPage{}, fmt.Errorf(
			"daemon returned invalid monitor event page: %w",
			err,
		)
	}
	return result, nil
}

// AcknowledgeMonitorEvent changes only one account-local queue item.
func (client *Client) AcknowledgeMonitorEvent(
	ctx context.Context,
	input application.MonitorAcknowledgeInput,
	caller domain.Caller,
) (application.MonitorEvent, error) {
	if err := input.Validate(); err != nil {
		return application.MonitorEvent{}, err
	}
	var result application.MonitorEvent
	if err := client.call(ctx, MethodEventAcknowledge, caller, input, &result); err != nil {
		return application.MonitorEvent{}, err
	}
	if result.ID != input.EventID || result.State != "acknowledged" {
		return application.MonitorEvent{}, errors.New("daemon returned invalid acknowledgement")
	}
	if err := result.Validate(input.Account); err != nil {
		return application.MonitorEvent{}, fmt.Errorf(
			"daemon returned invalid acknowledgement: %w",
			err,
		)
	}
	return result, nil
}

func validateSessionStatusResult(result SessionStatusResult) error {
	seen := make(map[domain.AccountID]struct{}, len(result.Accounts))
	for index, account := range result.Accounts {
		if err := account.Account.ValidateOpaque(); err != nil {
			return errors.New("daemon returned an invalid session account")
		}
		if err := domain.AccountAlias(account.Alias).Validate(); err != nil {
			return errors.New("daemon returned an invalid session account alias")
		}
		if err := account.Provider.Validate(); err != nil {
			return errors.New("daemon returned an invalid session provider")
		}
		if _, exists := seen[account.Account]; exists {
			return errors.New("daemon returned duplicate session accounts")
		}
		seen[account.Account] = struct{}{}
		if index > 0 && result.Accounts[index-1].Alias >= account.Alias {
			return errors.New("daemon returned unsorted session accounts")
		}
		switch account.State {
		case "authenticated":
			if !account.Authenticated || account.CapturedAt == nil || account.CapturedAt.IsZero() {
				return errors.New("daemon returned invalid authenticated session state")
			}
			if account.Capabilities == nil || account.Capabilities.Validate() != nil {
				return errors.New("daemon returned invalid account capabilities")
			}
			if len(account.Degradations) > 32 {
				return errors.New("daemon returned unbounded account degradations")
			}
			for _, degradation := range account.Degradations {
				if degradation.Validate() != nil {
					return errors.New("daemon returned invalid account degradation")
				}
			}
		case "pending", "signed_out":
			if account.Authenticated || account.CapturedAt != nil ||
				account.Capabilities != nil || len(account.Degradations) != 0 {
				return errors.New("daemon returned invalid inactive session state")
			}
		default:
			return errors.New("daemon returned unknown session state")
		}
	}
	return nil
}

// TerminalLogin starts or advances a caller-bound text-only browser login.
func (client *Client) TerminalLogin(
	ctx context.Context,
	input TerminalLoginInput,
	caller domain.Caller,
) (TerminalLoginResult, error) {
	if err := input.validate(); err != nil {
		return TerminalLoginResult{}, err
	}
	var result TerminalLoginResult
	if err := client.call(ctx, MethodTerminalLogin, caller, input, &result); err != nil {
		return TerminalLoginResult{}, err
	}
	if err := validateTerminalLoginResult(input, result); err != nil {
		return TerminalLoginResult{}, err
	}
	return result, nil
}

func validateTerminalLoginResult(input TerminalLoginInput, result TerminalLoginResult) error {
	if result.Account != input.Account {
		return errors.New("daemon returned terminal login state for a different account")
	}
	switch result.Status {
	case "pending":
		if !terminalSessionIDPattern.MatchString(result.SessionID) || result.View == nil ||
			!result.CapturedAt.IsZero() {
			return errors.New("daemon returned invalid pending terminal login state")
		}
		if input.SessionID != "" && result.SessionID != input.SessionID {
			return errors.New("daemon returned a different terminal login session")
		}
		for _, control := range result.View.Controls {
			if !terminalControlIDPattern.MatchString(control.ID) ||
				control.Kind != "input" && control.Kind != "activate" {
				return errors.New("daemon returned an invalid terminal login control")
			}
		}
		return nil
	case "authenticated":
		if result.CapturedAt.IsZero() || result.View != nil {
			return errors.New("daemon returned invalid authenticated terminal login state")
		}
		return nil
	case "cancelled":
		if !result.CapturedAt.IsZero() || result.View != nil {
			return errors.New("daemon returned invalid cancelled terminal login state")
		}
		return nil
	default:
		return errors.New("daemon returned an unknown terminal login status")
	}
}

func (client *Client) ListMail(ctx context.Context, input application.MailListInput, caller domain.Caller) (application.MailPage, error) {
	var result application.MailPage
	return result, client.call(ctx, MethodMailList, caller, input, &result)
}

// SearchMail executes one bounded, read-only AQS search through the session owner.
func (client *Client) SearchMail(ctx context.Context, input application.MailSearchInput, caller domain.Caller) (application.MailPage, error) {
	var result application.MailPage
	return result, client.call(ctx, MethodMailSearch, caller, input, &result)
}

// SearchAllMail returns one validated read-only projection without exposing
// daemon-owned account sessions to the caller.
func (client *Client) SearchAllMail(
	ctx context.Context,
	input application.MailProjectionInput,
	caller domain.Caller,
) (application.MailProjectionPage, error) {
	if err := input.Validate(); err != nil {
		return application.MailProjectionPage{}, err
	}
	var result application.MailProjectionPage
	if err := client.call(
		ctx,
		MethodMailSearchAll,
		caller,
		input,
		&result,
	); err != nil {
		return application.MailProjectionPage{}, err
	}
	if err := result.Validate(); err != nil {
		return application.MailProjectionPage{}, fmt.Errorf(
			"validate daemon mail projection: %w",
			err,
		)
	}
	return result, nil
}

// ListMailFolders discovers bounded folder metadata through the session owner.
func (client *Client) ListMailFolders(ctx context.Context, input application.MailFolderListInput, caller domain.Caller) (application.MailFolderPage, error) {
	var result application.MailFolderPage
	return result, client.call(ctx, MethodMailFolders, caller, input, &result)
}

func (client *Client) GetMailBody(ctx context.Context, input application.MailBodyInput, caller domain.Caller) (application.MailBodyAccess, error) {
	var result application.MailBodyAccess
	return result, client.call(ctx, MethodMailGetBody, caller, input, &result)
}

func (client *Client) CommitMailBody(ctx context.Context, token string, caller domain.Caller) (application.MailBodyAccess, error) {
	var result application.MailBodyAccess
	return result, client.call(ctx, MethodMailCommitBody, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) GetMailAttachment(ctx context.Context, input application.MailAttachmentInput, caller domain.Caller) (application.MailAttachmentAccess, error) {
	var result application.MailAttachmentAccess
	return result, client.call(ctx, MethodMailGetAttachment, caller, input, &result)
}

func (client *Client) CommitMailAttachment(ctx context.Context, token string, caller domain.Caller) (application.MailAttachmentAccess, error) {
	var result application.MailAttachmentAccess
	return result, client.call(ctx, MethodMailCommitAttachment, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) CreateMailDraft(ctx context.Context, input application.MailDraftInput, caller domain.Caller) (application.MailDraftAccess, error) {
	var result application.MailDraftAccess
	return result, client.call(ctx, MethodMailCreateDraft, caller, input, &result)
}

func (client *Client) CommitMailDraft(ctx context.Context, token string, caller domain.Caller) (application.MailDraftAccess, error) {
	var result application.MailDraftAccess
	return result, client.call(ctx, MethodMailCommitDraft, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) SendMail(ctx context.Context, input application.MailSendInput, caller domain.Caller) (application.MailSendAccess, error) {
	var result application.MailSendAccess
	return result, client.call(ctx, MethodMailSend, caller, input, &result)
}

func (client *Client) CommitMailSend(ctx context.Context, token string, caller domain.Caller) (application.MailSendAccess, error) {
	var result application.MailSendAccess
	return result, client.call(ctx, MethodMailCommitSend, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) MoveMail(ctx context.Context, input application.MailMoveInput, caller domain.Caller) (application.MailMoveAccess, error) {
	var result application.MailMoveAccess
	return result, client.call(ctx, MethodMailMove, caller, input, &result)
}

func (client *Client) CommitMailMove(ctx context.Context, token string, caller domain.Caller) (application.MailMoveAccess, error) {
	var result application.MailMoveAccess
	return result, client.call(ctx, MethodMailCommitMove, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) SetMailReadState(ctx context.Context, input application.MailReadStateInput, caller domain.Caller) (application.MailReadStateAccess, error) {
	var result application.MailReadStateAccess
	return result, client.call(ctx, MethodMailReadState, caller, input, &result)
}

func (client *Client) CommitMailReadState(ctx context.Context, token string, caller domain.Caller) (application.MailReadStateAccess, error) {
	var result application.MailReadStateAccess
	return result, client.call(ctx, MethodMailCommitState, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) DeleteMail(ctx context.Context, input application.MailDeleteInput, caller domain.Caller) (application.MailDeleteAccess, error) {
	var result application.MailDeleteAccess
	return result, client.call(ctx, MethodMailDelete, caller, input, &result)
}

func (client *Client) CommitMailDelete(ctx context.Context, token string, caller domain.Caller) (application.MailDeleteAccess, error) {
	var result application.MailDeleteAccess
	return result, client.call(ctx, MethodMailCommitDelete, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) ListCalendar(ctx context.Context, input application.CalendarListInput, caller domain.Caller) (application.CalendarPage, error) {
	var result application.CalendarPage
	return result, client.call(ctx, MethodCalendarList, caller, input, &result)
}

// ListAgenda returns one validated read-only cross-account event projection.
func (client *Client) ListAgenda(
	ctx context.Context,
	input application.AgendaProjectionInput,
	caller domain.Caller,
) (application.AgendaProjectionPage, error) {
	if err := input.Validate(); err != nil {
		return application.AgendaProjectionPage{}, err
	}
	var result application.AgendaProjectionPage
	if err := client.call(
		ctx,
		MethodAgendaList,
		caller,
		input,
		&result,
	); err != nil {
		return application.AgendaProjectionPage{}, err
	}
	if err := result.Validate(); err != nil {
		return application.AgendaProjectionPage{}, fmt.Errorf(
			"validate daemon agenda projection: %w",
			err,
		)
	}
	return result, nil
}

// CreateCalendar prepares an immutable calendar event preview.
func (client *Client) CreateCalendar(ctx context.Context, input application.CalendarCreateInput, caller domain.Caller) (application.CalendarCreateAccess, error) {
	var result application.CalendarCreateAccess
	return result, client.call(ctx, MethodCalendarCreate, caller, input, &result)
}

// CommitCalendarCreate consumes one caller-bound calendar event preview.
func (client *Client) CommitCalendarCreate(ctx context.Context, token string, caller domain.Caller) (application.CalendarCreateAccess, error) {
	var result application.CalendarCreateAccess
	return result, client.call(ctx, MethodCalendarCommit, caller, ApprovalInput{Token: token}, &result)
}

// UpdateCalendar prepares an immutable patch preview for one event version.
func (client *Client) UpdateCalendar(ctx context.Context, input application.CalendarUpdateInput, caller domain.Caller) (application.CalendarUpdateAccess, error) {
	var result application.CalendarUpdateAccess
	return result, client.call(ctx, MethodCalendarUpdate, caller, input, &result)
}

// CommitCalendarUpdate consumes one caller-bound calendar update preview.
func (client *Client) CommitCalendarUpdate(ctx context.Context, token string, caller domain.Caller) (application.CalendarUpdateAccess, error) {
	var result application.CalendarUpdateAccess
	return result, client.call(ctx, MethodCalendarCommitUpdate, caller, ApprovalInput{Token: token}, &result)
}

// CancelCalendar prepares a destructive preview for one event version.
func (client *Client) CancelCalendar(ctx context.Context, input application.CalendarCancelInput, caller domain.Caller) (application.CalendarCancelAccess, error) {
	var result application.CalendarCancelAccess
	return result, client.call(ctx, MethodCalendarCancel, caller, input, &result)
}

// CommitCalendarCancel consumes one caller-bound cancellation preview.
func (client *Client) CommitCalendarCancel(ctx context.Context, token string, caller domain.Caller) (application.CalendarCancelAccess, error) {
	var result application.CalendarCancelAccess
	return result, client.call(ctx, MethodCalendarCommitCancel, caller, ApprovalInput{Token: token}, &result)
}

func (client *Client) call(ctx context.Context, method Method, caller domain.Caller, input, output any) error {
	return client.callVersion(ctx, ProtocolVersion, method, caller, input, output)
}

func (client *Client) callVersion(
	ctx context.Context,
	protocolVersion int,
	method Method,
	caller domain.Caller,
	input, output any,
) error {
	credential, err := localipc.LoadCredential(client.endpoint)
	if err != nil {
		return fmt.Errorf("load daemon credential: %w", err)
	}
	return client.callWithCredential(
		ctx,
		protocolVersion,
		credential,
		method,
		caller,
		input,
		output,
	)
}

func (client *Client) callWithCredential(
	ctx context.Context,
	protocolVersion int,
	credential string,
	method Method,
	caller domain.Caller,
	input, output any,
) error {
	if !method.valid() {
		return errors.New("invalid daemon method")
	}
	if protocolVersion < 1 {
		return errors.New("invalid daemon protocol version")
	}
	if err := localipc.ValidateCredential(credential); err != nil {
		return errors.New("invalid daemon credential")
	}
	if err := caller.Validate(); err != nil {
		return err
	}
	params, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode daemon params: %w", err)
	}
	id, err := newRequestID()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(requestEnvelope{
		Version: protocolVersion, ID: id, Method: method, Caller: caller, Params: params,
	})
	if err != nil {
		return fmt.Errorf("encode daemon request: %w", err)
	}
	if len(payload) > maxRequestBytes {
		return errors.New("daemon request exceeds maximum size")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+client.host+requestPath, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build daemon request: %w", err)
	}
	request.Header.Set("Authorization", authorizationType+credential)
	request.Header.Set("Content-Type", contentType)
	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("call local daemon: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read daemon response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return errors.New("daemon response exceeds maximum size")
	}
	var envelope responseEnvelope
	if err := decodeStrict(bytes.NewReader(body), &envelope); err != nil {
		return errors.New("daemon returned an invalid response")
	}
	if envelope.ID != id && envelope.ID != "" {
		return errors.New("daemon returned a mismatched response")
	}
	if envelope.Version != protocolVersion {
		if envelope.Version < 1 {
			return errors.New("daemon returned an invalid response")
		}
		rejected := response.StatusCode == http.StatusBadRequest &&
			envelope.ID == id &&
			envelope.Error != nil &&
			envelope.Error.Code == "invalid_request" &&
			envelope.Error.Message == fmt.Sprintf(
				"unsupported daemon protocol version %d",
				protocolVersion,
			)
		return &ProtocolVersionError{
			ClientVersion: protocolVersion,
			DaemonVersion: envelope.Version,
			rejected:      rejected,
		}
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if response.StatusCode != http.StatusOK || len(envelope.Result) == 0 {
		return fmt.Errorf("daemon returned HTTP %d without a result", response.StatusCode)
	}
	if err := decodeStrict(bytes.NewReader(envelope.Result), output); err != nil {
		return errors.New("daemon returned an invalid result")
	}
	return nil
}

func newRequestID() (string, error) {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate daemon request ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}
