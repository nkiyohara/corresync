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
	done       chan struct{}
}

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

// Run returns after completion, cancellation, expiry, or server failure.
func (server *Server) Run(ctx context.Context) error {
	failure := make(chan error, 1)
	go func() { failure <- server.server.Serve(server.listener) }()
	var result error
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case <-server.done:
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
		http.Error(writer, "login link expired or invalid", http.StatusUnauthorized)
		return
	}
	if server.controller != "" && server.controller != controller {
		http.Error(writer, "login already controlled by another tab", http.StatusConflict)
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
		server.started = true
	} else {
		result, err = server.exchange(ctx, &action)
	}
	if err != nil {
		http.Error(writer, "login interaction failed; refresh only, do not automatically repeat input", http.StatusBadGateway)
		return
	}
	if result.Frame != nil && len(result.Frame.JPEG) > application.MaxGraphicalLoginFrameBytes {
		http.Error(writer, "frame exceeds bound", http.StatusBadGateway)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(result)
	if result.Status == "authenticated" || result.Status == "cancelled" {
		server.finished = true
		close(server.done)
	}
}
