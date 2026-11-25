// Package loginui serves a temporary human-operated login viewport on loopback.
// It never exposes the daemon API, CDP, or provider credentials.
package loginui

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
)

//go:embed index.html
var indexHTML []byte

//go:embed client.js
var clientJS []byte

// Result is one answer from the remote login. It is also the SSH helper's
// wire result, so both optional fields are omitted when unset and an older
// local side, which ignores them, still works.
type Result struct {
	Status string                           `json:"status"`
	Frame  *application.GraphicalLoginFrame `json:"frame,omitempty"`
	// Unchanged means the remote screen is byte-identical to the frame sent
	// last, so Frame is omitted. It is never set together with Frame.
	Unchanged bool `json:"unchanged,omitempty"`
	// ExpiresIn is the remote helper's remaining lifetime in whole seconds.
	ExpiresIn int `json:"expiresIn,omitempty"`
}

type Exchange func(context.Context, *application.GraphicalLoginAction) (Result, error)

// pageResult is the loopback page's view of a Result. FrameID names the frame
// the server holds; when the page already shows it, Frame is omitted.
type pageResult struct {
	Status    string                           `json:"status"`
	Frame     *application.GraphicalLoginFrame `json:"frame,omitempty"`
	FrameID   uint64                           `json:"frameId,omitempty"`
	Unchanged bool                             `json:"unchanged,omitempty"`
	ExpiresIn int                              `json:"expiresIn,omitempty"`
}

type Server struct {
	server     *http.Server
	listener   net.Listener
	token      string
	port       int
	exchange   Exchange
	mu         sync.Mutex
	controller string
	started    bool
	finished   bool
	outcome    string
	failure    error
	unreadable int
	expires    time.Time
	// The last frame is kept in memory only so an unchanged screen need not be
	// sent again, and so a reloaded tab can get it back. It is dropped when the
	// view ends.
	frame   *application.GraphicalLoginFrame
	frameID uint64
	done    chan struct{}
}

// maxRemoteLifetime bounds the remaining lifetime a remote helper may report.
const maxRemoteLifetime = 10 * time.Minute

// maxUnreadableScreens is how many screen reads in a row may fail before the
// view gives up. One read per second means the remote login is gone: it
// expired, or a newer login for the same account replaced it.
const maxUnreadableScreens = 5

// New binds the view to loopback. The deadline of ctx, if any, is the time
// the page shows as the link's remaining lifetime.
func New(ctx context.Context, port int, exchange Exchange) (*Server, error) {
	if port < 0 || port > 65535 || exchange == nil {
		return nil, errors.New("invalid login UI options")
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, errors.New("cannot bind the loopback login port; choose a free --web-port")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		_ = listener.Close()
		return nil, err
	}
	result := &Server{listener: listener, port: listener.Addr().(*net.TCPAddr).Port, token: hex.EncodeToString(random[:]), exchange: exchange, done: make(chan struct{})}
	if deadline, ok := ctx.Deadline(); ok {
		result.expires = deadline
	}
	result.server = &http.Server{Handler: result, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8 << 10}
	return result, nil
}

func (server *Server) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/#%s", server.port, server.token)
}
func (server *Server) Port() int { return server.port }

// Outcome reports how the interaction ended: "authenticated", "cancelled", or
// empty when Run returned for another reason.
func (server *Server) Outcome() string {
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.outcome
}

// Run returns after completion, cancellation, expiry, or server failure.
func (server *Server) Run(ctx context.Context) error {
	failure := make(chan error, 1)
	go func() { failure <- server.server.Serve(server.listener) }()
	var result error
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case <-server.done:
		server.mu.Lock()
		result = server.failure
		server.mu.Unlock()
	case result = <-failure:
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = server.server.Shutdown(shutdown)
	_ = server.server.Close()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.started && !server.finished {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		_, _ = server.exchange(cleanup, &application.GraphicalLoginAction{Type: "cancel"})
	}
	server.token = ""
	server.frame = nil
	return result
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; img-src data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	host := "127.0.0.1:" + strconv.Itoa(server.port)
	if request.Host != host && request.Host != "localhost:"+strconv.Itoa(server.port) {
		http.Error(writer, "invalid host", http.StatusForbidden)
		return
	}
	if request.URL.RawQuery != "" {
		http.Error(writer, "queries are not accepted", http.StatusBadRequest)
		return
	}
	if request.URL.Path == "/" || request.URL.Path == "/client.js" {
		if request.Method != http.MethodGet {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if request.URL.Path == "/" {
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = writer.Write(indexHTML)
		} else {
			writer.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = writer.Write(clientJS)
		}
		return
	}
	if request.URL.Path != "/action" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.Header.Get("Origin") != "http://"+request.Host {
		http.Error(writer, "invalid origin", http.StatusForbidden)
		return
	}
	if request.Header.Get("Content-Type") != "application/json" {
		http.Error(writer, "JSON required", http.StatusUnsupportedMediaType)
		return
	}
	bearer := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	controller := request.Header.Get("X-Login-Controller")
	if len(controller) != 32 {
		http.Error(writer, "invalid controller", http.StatusForbidden)
		return
	}
	if _, err := hex.DecodeString(controller); err != nil {
		http.Error(writer, "invalid controller", http.StatusForbidden)
		return
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.finished || server.token == "" || subtle.ConstantTimeCompare([]byte(bearer), []byte(server.token)) != 1 {
		fail(writer, http.StatusUnauthorized, "expired", "This link has expired or is no longer valid. Run the corr command again for a new link.")
		return
	}
	if server.controller != "" && server.controller != controller {
		fail(writer, http.StatusConflict, "elsewhere", "Another browser tab is using this link. Continue there, or cancel and run the corr command again.")
		return
	}
	// The frame the page shows now, so an unchanged screen is not sent again.
	// A reloaded tab sends none and gets the current frame.
	shown := uint64(0)
	if value := request.Header.Get("X-Login-Frame"); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || len(value) > 20 {
			http.Error(writer, "invalid input", http.StatusBadRequest)
			return
		}
		shown = parsed
	}
	var action application.GraphicalLoginAction
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&action); err != nil {
		http.Error(writer, "invalid input", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(writer, "invalid input", http.StatusBadRequest)
		return
	}
	if err := action.Validate(); err != nil {
		http.Error(writer, "invalid input", http.StatusBadRequest)
		return
	}
	if !server.started && action.Type != "refresh" {
		http.Error(writer, "open the login view first", http.StatusConflict)
		return
	}
	server.controller = controller
	ctx, cancel := context.WithTimeout(request.Context(), 25*time.Second)
	defer cancel()
	var result Result
	var err error
	if !server.started {
		result, err = server.exchange(ctx, nil)
		// A failed start is not retried automatically: its outcome may be unknown.
		// Nothing in this view can recover from it, so the command ends with the
		// reason instead of serving a dead page until the link expires.
		server.started = true
		if err != nil {
			server.finished = true
			server.failure = err
			close(server.done)
			fail(writer, http.StatusBadGateway, "start-failed", "The login could not start. The terminal running corr shows why.")
			return
		}
	} else {
		result, err = server.exchange(ctx, &action)
	}
	if err != nil {
		if action.Type == "refresh" {
			server.unreadable++
			if server.unreadable < maxUnreadableScreens {
				fail(writer, http.StatusBadGateway, "unreadable", "The remote screen could not be read.")
				return
			}
			// Close whatever is left of the remote login, then end the command
			// with the reason instead of serving a view that cannot recover.
			cleanup, stop := context.WithTimeout(context.WithoutCancel(request.Context()), 3*time.Second)
			_, _ = server.exchange(cleanup, &application.GraphicalLoginAction{Type: "cancel"})
			stop()
			server.finished = true
			server.failure = fmt.Errorf("the remote login ended before sign-in completed; run the command again: %w", err)
			close(server.done)
			fail(writer, http.StatusBadGateway, "ended", "This login has ended. The terminal running corr shows why.")
			return
		}
		if action.Type == "move" {
			// A pointer move that did not arrive is dropped, not reported.
			fail(writer, http.StatusBadGateway, "dropped", "The pointer move was not delivered.")
			return
		}
		fail(writer, http.StatusBadGateway, "input-unknown", "The last input may not have arrived. Press Refresh view and look at the screen before you continue.")
		return
	}
	server.unreadable = 0
	if result.Frame != nil && (len(result.Frame.JPEG) == 0 || len(result.Frame.JPEG) > application.MaxGraphicalLoginFrameBytes) {
		server.frame = nil
		http.Error(writer, "frame exceeds bound", http.StatusBadGateway)
		return
	}
	if result.Frame == nil && result.Unchanged && server.frame != nil {
		result.Frame = server.frame
	}
	if result.ExpiresIn > 0 && time.Duration(result.ExpiresIn)*time.Second <= maxRemoteLifetime {
		if remote := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second); server.expires.IsZero() || remote.Before(server.expires) {
			server.expires = remote
		}
	}
	response := pageResult{Status: result.Status}
	if !server.expires.IsZero() {
		response.ExpiresIn = max(0, int((time.Until(server.expires)+time.Second-1)/time.Second))
	}
	if result.Frame != nil {
		if !sameFrame(server.frame, result.Frame) {
			server.frame = result.Frame
			server.frameID++
		}
		response.FrameID = server.frameID
		if shown == server.frameID {
			response.Unchanged = true
		} else {
			response.Frame = server.frame
		}
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(response)
	if result.Status == "authenticated" || result.Status == "cancelled" {
		server.finished = true
		server.outcome = result.Status
		server.frame = nil
		close(server.done)
	}
}

// fail answers the page with one plain sentence and a fixed state name the
// page uses to choose what to show. Neither carries remote content.
func fail(writer http.ResponseWriter, status int, state, message string) {
	writer.Header().Set("X-Login-State", state)
	http.Error(writer, message, status)
}

// sameFrame reports whether two frames show the same picture of the same
// origin. The origin is compared too: the label must always match the page.
func sameFrame(previous, next *application.GraphicalLoginFrame) bool {
	return previous != nil && next != nil && previous.Origin == next.Origin &&
		previous.Width == next.Width && previous.Height == next.Height && bytes.Equal(previous.JPEG, next.JPEG)
}
