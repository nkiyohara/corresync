// Package loginui serves a temporary human-operated login viewport on loopback.
// It never exposes the daemon API, CDP, or provider credentials.
package loginui

import (
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

type Result struct {
	Status string                           `json:"status"`
	Frame  *application.GraphicalLoginFrame `json:"frame,omitempty"`
}

type Exchange func(context.Context, *application.GraphicalLoginAction) (Result, error)

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
	done       chan struct{}
}

// maxUnreadableScreens is how many screen reads in a row may fail before the
// view gives up. One read per second means the remote login is gone: it
// expired, or a newer login for the same account replaced it.
const maxUnreadableScreens = 5

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
		http.Error(writer, "This login link has expired or is not valid. Run the corr command again for a new link.", http.StatusUnauthorized)
		return
	}
	if server.controller != "" && server.controller != controller {
		http.Error(writer, "Another browser tab already controls this login. Continue there, or cancel and run the corr command again.", http.StatusConflict)
		return
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
			http.Error(writer, "The login could not start. The terminal running corr shows the reason.", http.StatusBadGateway)
			return
		}
	} else {
		result, err = server.exchange(ctx, &action)
	}
	if err != nil {
		if action.Type == "refresh" {
			server.unreadable++
			if server.unreadable < maxUnreadableScreens {
				http.Error(writer, "The remote screen could not be read.", http.StatusBadGateway)
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
			http.Error(writer, "This login has ended. The terminal running corr shows the reason.", http.StatusBadGateway)
			return
		}
		http.Error(writer, "The last input may not have reached the remote page. Check the screen before typing again; input is never repeated automatically.", http.StatusBadGateway)
		return
	}
	server.unreadable = 0
	if result.Frame != nil && len(result.Frame.JPEG) > application.MaxGraphicalLoginFrameBytes {
		http.Error(writer, "frame exceeds bound", http.StatusBadGateway)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(result)
	if result.Status == "authenticated" || result.Status == "cancelled" {
		server.finished = true
		server.outcome = result.Status
		close(server.done)
	}
}
