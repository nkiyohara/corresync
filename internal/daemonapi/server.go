package daemonapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/localipc"
)

const maxConcurrentCalls = 32

// ServerOptions identifies the process serving one API namespace.
type ServerOptions struct {
	Version      string
	ProcessID    int
	StartedAt    time.Time
	Credential   string
	ConfigDigest string
}

// Server hosts the daemon API over a caller-provided local-only listener.
type Server struct {
	backend  Backend
	status   Status
	token    string
	slots    chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	http     *http.Server
}

// NewServer constructs a fail-closed handler with bounded concurrency.
func NewServer(backend Backend, options ServerOptions) (*Server, error) {
	if backend == nil {
		return nil, errors.New("daemon backend is required")
	}
	if options.Version == "" || options.ProcessID < 1 {
		return nil, errors.New("daemon version and process ID are required")
	}
	if options.StartedAt.IsZero() {
		return nil, errors.New("daemon start time is required")
	}
	if err := validateConfigDigest(options.ConfigDigest); err != nil {
		return nil, err
	}
	if err := localipc.ValidateCredential(options.Credential); err != nil {
		return nil, err
	}
	if err := backend.DefaultAccount().Validate(); err != nil {
		return nil, fmt.Errorf("validate default daemon account: %w", err)
	}
	server := &Server{
		backend: backend,
		status: Status{
			ProtocolVersion: ProtocolVersion, Version: options.Version,
			ProcessID: options.ProcessID, StartedAt: options.StartedAt.UTC(),
			DefaultAccount: backend.DefaultAccount(),
			ConfigDigest:   options.ConfigDigest,
		},
		token: options.Credential,
		slots: make(chan struct{}, maxConcurrentCalls),
		stop:  make(chan struct{}),
	}
	server.http = &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	return server, nil
}

// Serve accepts HTTP only from the platform local listener.
func (server *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("daemon listener is required")
	}
	err := server.http.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Shutdown drains active requests within the caller's deadline.
func (server *Server) Shutdown(ctx context.Context) error { return server.http.Shutdown(ctx) }

// Done closes after an authenticated local client requests shutdown.
func (server *Server) Done() <-chan struct{} { return server.stop }

// ServeHTTP authenticates before reading a potentially sensitive body.
func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", contentType)
	if request.Method != http.MethodPost || request.URL.Path != requestPath ||
		request.URL.RawQuery != "" || request.Host != requestHost {
		server.writeFailure(writer, http.StatusNotFound, "", "not_found", "daemon endpoint not found")
		return
	}
	if !server.authorized(request.Header.Get("Authorization")) {
		server.writeFailure(writer, http.StatusUnauthorized, "", "unauthorized", "daemon authorization failed")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != contentType {
		server.writeFailure(writer, http.StatusUnsupportedMediaType, "", "invalid_request", "content type must be application/json")
		return
	}
	select {
	case server.slots <- struct{}{}:
		defer func() { <-server.slots }()
	default:
		server.writeFailure(writer, http.StatusServiceUnavailable, "", "busy", "daemon concurrency limit reached")
		return
	}

	limited := http.MaxBytesReader(writer, request.Body, maxRequestBytes)
	defer func() { _ = limited.Close() }()
	var envelope requestEnvelope
	if err := decodeStrict(limited, &envelope); err != nil {
		server.writeFailure(writer, http.StatusBadRequest, "", "invalid_request", "invalid daemon request")
		return
	}
	if err := envelope.validate(); err != nil {
		server.writeFailure(writer, http.StatusBadRequest, envelope.ID, "invalid_request", err.Error())
		return
	}
	result, callErr := server.dispatch(request.Context(), envelope)
	if callErr != nil {
		server.writeFailure(writer, http.StatusOK, envelope.ID, "operation_failed", callErr.Error())
		return
	}
	server.writeResult(writer, envelope.ID, result)
	if envelope.Method == MethodShutdown {
		server.stopOnce.Do(func() { close(server.stop) })
	}
}

func (server *Server) authorized(header string) bool {
	if !strings.HasPrefix(header, authorizationType) {
		return false
	}
	return localipc.MatchesCredential(strings.TrimPrefix(header, authorizationType), server.token)
}

func (server *Server) dispatch(ctx context.Context, request requestEnvelope) (any, error) {
	switch request.Method {
	case MethodStatus:
		var input struct{}
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.status, nil
	case MethodShutdown:
		var input struct{}
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return struct {
			Stopping bool `json:"stopping"`
		}{Stopping: true}, nil
	case MethodLogin:
		var input LoginInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		if err := input.Account.Validate(); err != nil {
			return nil, err
		}
		return server.backend.Login(ctx, input.Account, request.Caller)
	case MethodSessionStatus:
		var input struct{}
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.SessionStatus(ctx, request.Caller)
	case MethodMonitorStatus:
		var input MonitorStatusInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		if err := input.Account.ValidateOpaque(); err != nil {
			return nil, err
		}
		backend, supported := server.backend.(MonitoringBackend)
		if !supported {
			return nil, errors.New("monitoring is not supported by this session owner")
		}
		return backend.MonitorStatus(ctx, input.Account, request.Caller)
	case MethodEventsList:
		var input application.MonitorEventListInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		if err := input.Validate(); err != nil {
			return nil, err
		}
		backend, supported := server.backend.(MonitoringBackend)
		if !supported {
			return nil, errors.New("monitoring is not supported by this session owner")
		}
		return backend.ListMonitorEvents(ctx, input, request.Caller)
	case MethodEventAcknowledge:
		var input application.MonitorAcknowledgeInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		if err := input.Validate(); err != nil {
			return nil, err
		}
		backend, supported := server.backend.(MonitoringBackend)
		if !supported {
			return nil, errors.New("monitoring is not supported by this session owner")
		}
		return backend.AcknowledgeMonitorEvent(ctx, input, request.Caller)
	case MethodTerminalLogin:
		var input TerminalLoginInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		if err := input.validate(); err != nil {
			return nil, err
		}
		backend, supported := server.backend.(TerminalLoginBackend)
		if !supported {
			return nil, errors.New("terminal login is not supported by this session owner")
		}
		return backend.TerminalLogin(ctx, input, request.Caller)
	case MethodMailList:
		var input application.MailListInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.ListMail(ctx, input, request.Caller)
	case MethodMailSearch:
		var input application.MailSearchInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.SearchMail(ctx, input, request.Caller)
	case MethodMailSearchAll:
		var input application.MailProjectionInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.SearchAllMail(ctx, input, request.Caller)
	case MethodMailFolders:
		var input application.MailFolderListInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.ListMailFolders(ctx, input, request.Caller)
	case MethodMailGetBody:
		var input application.MailBodyInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.GetMailBody(ctx, input, request.Caller)
	case MethodMailCommitBody:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitMailBody(ctx, input.Token, request.Caller)
	case MethodMailGetAttachment:
		var input application.MailAttachmentInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.GetMailAttachment(ctx, input, request.Caller)
	case MethodMailCommitAttachment:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitMailAttachment(ctx, input.Token, request.Caller)
	case MethodMailCreateDraft:
		var input application.MailDraftInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CreateMailDraft(ctx, input, request.Caller)
	case MethodMailCommitDraft:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitMailDraft(ctx, input.Token, request.Caller)
	case MethodMailSend:
		var input application.MailSendInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.SendMail(ctx, input, request.Caller)
	case MethodMailCommitSend:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitMailSend(ctx, input.Token, request.Caller)
	case MethodMailMove:
		var input application.MailMoveInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.MoveMail(ctx, input, request.Caller)
	case MethodMailCommitMove:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitMailMove(ctx, input.Token, request.Caller)
	case MethodMailReadState:
		var input application.MailReadStateInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.SetMailReadState(ctx, input, request.Caller)
	case MethodMailCommitState:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitMailReadState(ctx, input.Token, request.Caller)
	case MethodMailDelete:
		var input application.MailDeleteInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.DeleteMail(ctx, input, request.Caller)
	case MethodMailCommitDelete:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitMailDelete(ctx, input.Token, request.Caller)
	case MethodCalendarList:
		var input application.CalendarListInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.ListCalendar(ctx, input, request.Caller)
	case MethodAgendaList:
		var input application.AgendaProjectionInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.ListAgenda(ctx, input, request.Caller)
	case MethodCalendarCreate:
		var input application.CalendarCreateInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CreateCalendar(ctx, input, request.Caller)
	case MethodCalendarCommit:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitCalendarCreate(ctx, input.Token, request.Caller)
	case MethodCalendarUpdate:
		var input application.CalendarUpdateInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.UpdateCalendar(ctx, input, request.Caller)
	case MethodCalendarCommitUpdate:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitCalendarUpdate(ctx, input.Token, request.Caller)
	case MethodCalendarCancel:
		var input application.CalendarCancelInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CancelCalendar(ctx, input, request.Caller)
	case MethodCalendarCommitCancel:
		var input ApprovalInput
		if err := decodeStrict(bytes.NewReader(request.Params), &input); err != nil {
			return nil, err
		}
		return server.backend.CommitCalendarCancel(ctx, input.Token, request.Caller)
	default:
		return nil, errors.New("unknown daemon method")
	}
}

func (server *Server) writeResult(writer http.ResponseWriter, id string, result any) {
	encoded, err := json.Marshal(result)
	if err != nil {
		server.writeFailure(writer, http.StatusInternalServerError, id, "internal_error", "encode daemon result")
		return
	}
	server.writeEnvelope(writer, http.StatusOK, responseEnvelope{
		Version: ProtocolVersion, ID: id, Result: encoded,
	})
}

func (server *Server) writeFailure(writer http.ResponseWriter, status int, id, code, message string) {
	server.writeEnvelope(writer, status, responseEnvelope{
		Version: ProtocolVersion, ID: id, Error: &Error{Code: code, Message: message},
	})
}

func (server *Server) writeEnvelope(writer http.ResponseWriter, status int, envelope responseEnvelope) {
	encoded, err := json.Marshal(envelope)
	if err != nil || len(encoded) > maxResponseBytes {
		status = http.StatusInternalServerError
		encoded = []byte(fmt.Sprintf(
			`{"version":%d,"error":{"code":"internal_error","message":"daemon response unavailable"}}`,
			ProtocolVersion,
		))
	}
	writer.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	writer.WriteHeader(status)
	_, _ = writer.Write(encoded)
}

func decodeStrict(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
