package imapmail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
)

const (
	maximumIMAPControlLineBytes = 64 << 10
	maximumIMAPResponseControl  = 1 << 20
	maximumIMAPOperationBytes   = 32 << 20
	maximumIMAPUpgradeLines     = 100
	maximumIMAPParserCPU        = 5 * time.Second
	imapStartTLSTag             = "C0"
)

func dialBoundedIMAP(
	ctx context.Context,
	dialer *net.Dialer,
	address string,
	mode string,
	tlsConfig *tls.Config,
) (*imapclient.Client, error) {
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	closeOnFailure := true
	defer func() {
		if closeOnFailure {
			_ = raw.Close()
		}
	}()
	if err := raw.SetDeadline(time.Now().Add(networkTimeout)); err != nil {
		return nil, err
	}

	var encrypted net.Conn
	var greeting []byte
	switch mode {
	case TLSImplicit:
		secure := tls.Client(raw, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		encrypted = secure
	case TLSStartTLS:
		encrypted, err = negotiateIMAPStartTLS(ctx, raw, tlsConfig)
		if err != nil {
			return nil, err
		}
		// The library constructor expects a greeting. The real greeting was
		// consumed before STARTTLS, so inject a fixed local greeting after the
		// authenticated TLS handshake. It immediately re-queries capabilities.
		greeting = []byte("* OK Corresync established bounded TLS\r\n")
	default:
		return nil, errors.New("unsupported IMAP TLS mode")
	}
	bounded, err := newBoundedIMAPConn(
		encrypted,
		maximumIMAPLiteralBytes,
		greeting,
	)
	if err != nil {
		return nil, err
	}
	connection, err := imapclient.New(bounded)
	if err != nil {
		return nil, err
	}
	closeOnFailure = false
	return connection, nil
}

func negotiateIMAPStartTLS(
	ctx context.Context,
	raw net.Conn,
	tlsConfig *tls.Config,
) (net.Conn, error) {
	reader := bufio.NewReaderSize(raw, maximumIMAPControlLineBytes+1)
	greeting, err := readIMAPControlLine(reader)
	if err != nil {
		return nil, fmt.Errorf("read IMAP greeting before STARTTLS: %w", err)
	}
	if !bytes.HasPrefix(bytes.ToUpper(greeting), []byte("* OK")) {
		return nil, errors.New("IMAP server did not provide a pre-TLS OK greeting")
	}
	if _, err := io.WriteString(raw, imapStartTLSTag+" STARTTLS\r\n"); err != nil {
		return nil, fmt.Errorf("request IMAP STARTTLS: %w", err)
	}
	accepted := false
	for range maximumIMAPUpgradeLines {
		line, err := readIMAPControlLine(reader)
		if err != nil {
			return nil, fmt.Errorf("read IMAP STARTTLS response: %w", err)
		}
		if !bytes.HasPrefix(line, []byte(imapStartTLSTag+" ")) {
			continue
		}
		if !bytes.HasPrefix(
			bytes.ToUpper(line),
			[]byte(imapStartTLSTag+" OK"),
		) {
			return nil, errors.New("IMAP server rejected STARTTLS")
		}
		accepted = true
		break
	}
	if !accepted {
		return nil, errors.New("IMAP STARTTLS response exceeded its line bound")
	}
	secure := tls.Client(&bufferedNetConn{Conn: raw, reader: reader}, tlsConfig)
	if err := secure.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("complete IMAP STARTTLS handshake: %w", err)
	}
	return secure, nil
}

func readIMAPControlLine(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, errors.New("IMAP control line exceeds its size bound")
	}
	if err != nil {
		return nil, err
	}
	if !bytes.HasSuffix(line, []byte("\r\n")) {
		return nil, errors.New("IMAP control line does not use CRLF")
	}
	if _, literal, literalErr := imapLiteralSize(line); literalErr != nil {
		return nil, literalErr
	} else if literal {
		return nil, errors.New("IMAP control exchange unexpectedly contains a literal")
	}
	return append([]byte(nil), line...), nil
}

type bufferedNetConn struct {
	net.Conn
	reader *bufio.Reader
}

func (connection *bufferedNetConn) Read(destination []byte) (int, error) {
	return connection.reader.Read(destination)
}

type boundedIMAPConn struct {
	net.Conn
	maximumLiteral uint64

	readMu        sync.Mutex
	reader        *bufio.Reader
	pending       []byte
	terminalErr   error
	errorReturned bool
	maximumTotal  uint64
	literalTotal  uint64
}

func newBoundedIMAPConn(
	connection net.Conn,
	maximumLiteral int,
	prefix []byte,
) (*boundedIMAPConn, error) {
	if connection == nil || maximumLiteral < 1 {
		return nil, errors.New("bounded IMAP connection requires a transport and limit")
	}
	return &boundedIMAPConn{
		Conn:           connection,
		maximumLiteral: uint64(maximumLiteral),
		maximumTotal:   maximumIMAPOperationBytes,
		pending:        append([]byte(nil), prefix...),
		reader: bufio.NewReaderSize(
			connection,
			maximumIMAPControlLineBytes+1,
		),
	}, nil
}

func (connection *boundedIMAPConn) Read(destination []byte) (int, error) {
	connection.readMu.Lock()
	defer connection.readMu.Unlock()
	if len(destination) == 0 {
		return 0, nil
	}
	for len(connection.pending) == 0 && connection.terminalErr == nil {
		if err := connection.readResponse(); err != nil {
			connection.terminalErr = err
			_ = connection.Close()
		}
	}
	if len(connection.pending) > 0 {
		count := copy(destination, connection.pending)
		connection.pending = connection.pending[count:]
		return count, nil
	}
	if connection.terminalErr != nil && !connection.errorReturned {
		connection.errorReturned = true
		return 0, connection.terminalErr
	}
	return 0, io.EOF
}

func (connection *boundedIMAPConn) readResponse() error {
	if connection.maximumLiteral > uint64(^uint32(0)) {
		return errors.New("IMAP literal limit exceeds the parser range")
	}
	capture := &imapResponseCapture{
		source:       connection.reader,
		maximumTotal: connection.maximumTotal,
		literalTotal: connection.literalTotal,
		cpuDeadline:  time.Now().Add(maximumIMAPParserCPU),
	}
	parser := imap.NewReader(capture)
	parser.MaxLiteralSize = uint32(connection.maximumLiteral)
	if _, err := imap.ReadResp(parser); err != nil {
		if errors.Is(err, io.EOF) && capture.bytes.Len() == 0 {
			return io.EOF
		}
		return fmt.Errorf(
			"validate bounded IMAP response after %d control and %d literal bytes: %w",
			capture.controlTotal,
			capture.literalTotal,
			err,
		)
	}
	connection.literalTotal = capture.literalTotal
	connection.pending = append(connection.pending, capture.bytes.Bytes()...)
	return nil
}

// imapResponseCapture gives the pinned go-imap parser the authoritative view
// of one response while preserving the exact bytes for the real client. Parser
// control reads and literal payload reads use different interface methods, so
// grammar, CRLF, line, aggregate-control, literal, and CPU bounds are enforced
// in one forward-only pass.
type imapResponseCapture struct {
	source       *bufio.Reader
	bytes        bytes.Buffer
	maximumTotal uint64
	literalTotal uint64
	controlTotal int
	controlLine  int
	previous     byte
	cpuDeadline  time.Time

	lastRuneBytes    int
	lastControlLine  int
	lastControlTotal int
	lastPrevious     byte
}

func (capture *imapResponseCapture) Read(destination []byte) (int, error) {
	capture.lastRuneBytes = 0
	if len(destination) == 0 {
		return 0, nil
	}
	if capture.literalTotal >= capture.maximumTotal {
		return 0, fmt.Errorf(
			"IMAP response literals exceed the %d-byte operation limit",
			capture.maximumTotal,
		)
	}
	remaining := capture.maximumTotal - capture.literalTotal
	exhaustsBudget := uint64(len(destination)) >= remaining
	if uint64(len(destination)) > remaining {
		destination = destination[:remaining]
	}
	started := time.Now()
	count, err := capture.source.Read(destination)
	capture.cpuDeadline = capture.cpuDeadline.Add(time.Since(started))
	if count > 0 {
		capture.literalTotal += uint64(count)
		_, _ = capture.bytes.Write(destination[:count])
	}
	if err == nil && exhaustsBudget && count == len(destination) {
		return count, fmt.Errorf(
			"IMAP response literals exceed the %d-byte operation limit",
			capture.maximumTotal,
		)
	}
	return count, err
}

func (capture *imapResponseCapture) ReadRune() (rune, int, error) {
	if err := capture.checkCPUBudget(); err != nil {
		return 0, 0, err
	}
	capture.lastControlLine = capture.controlLine
	capture.lastControlTotal = capture.controlTotal
	capture.lastPrevious = capture.previous
	started := time.Now()
	character, size, err := capture.source.ReadRune()
	capture.cpuDeadline = capture.cpuDeadline.Add(time.Since(started))
	if err != nil {
		return 0, 0, err
	}
	if character == utf8.RuneError && size == 1 {
		return 0, 0, errors.New("IMAP response control data is not valid UTF-8")
	}
	var encoded [utf8.UTFMax]byte
	count := utf8.EncodeRune(encoded[:], character)
	if count != size {
		return 0, 0, errors.New("IMAP response control rune encoding changed")
	}
	raw := encoded[:count]
	if err := capture.acceptControl(raw); err != nil {
		return 0, 0, err
	}
	capture.lastRuneBytes = len(raw)
	return character, size, nil
}

func (capture *imapResponseCapture) UnreadRune() error {
	if capture.lastRuneBytes == 0 {
		return errors.New("IMAP parser attempted an invalid rune rewind")
	}
	if err := capture.source.UnreadRune(); err != nil {
		return err
	}
	capture.bytes.Truncate(capture.bytes.Len() - capture.lastRuneBytes)
	capture.controlLine = capture.lastControlLine
	capture.controlTotal = capture.lastControlTotal
	capture.previous = capture.lastPrevious
	capture.lastRuneBytes = 0
	return nil
}

func (capture *imapResponseCapture) ReadString(delimiter byte) (string, error) {
	capture.lastRuneBytes = 0
	var result bytes.Buffer
	for {
		if err := capture.checkCPUBudget(); err != nil {
			return result.String(), err
		}
		started := time.Now()
		fragment, err := capture.source.ReadSlice(delimiter)
		capture.cpuDeadline = capture.cpuDeadline.Add(time.Since(started))
		if len(fragment) > 0 {
			if controlErr := capture.acceptControl(fragment); controlErr != nil {
				return result.String(), controlErr
			}
			_, _ = result.Write(fragment)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return result.String(), err
		}
	}
}

func (capture *imapResponseCapture) acceptControl(input []byte) error {
	for _, character := range input {
		capture.controlTotal++
		capture.controlLine++
		if capture.controlTotal > maximumIMAPResponseControl {
			return errors.New(
				"IMAP response control data exceeds its aggregate size bound",
			)
		}
		if capture.controlLine > maximumIMAPControlLineBytes {
			return errors.New("IMAP response control line exceeds its size bound")
		}
		if character == '\n' {
			if capture.previous != '\r' {
				return errors.New("IMAP response control line does not use CRLF")
			}
			capture.controlLine = 0
		}
		capture.previous = character
	}
	_, _ = capture.bytes.Write(input)
	return capture.checkCPUBudget()
}

func (capture *imapResponseCapture) checkCPUBudget() error {
	if time.Now().After(capture.cpuDeadline) {
		return errors.New("IMAP response parsing exceeded its CPU time bound")
	}
	return nil
}

func imapLiteralSize(line []byte) (uint64, bool, error) {
	if len(line) == 0 || line[len(line)-1] != '\n' {
		return 0, false, nil
	}
	end := len(line) - 1
	if end > 0 && line[end-1] == '\r' {
		end--
	}
	if end == 0 || line[end-1] != '}' {
		return 0, false, nil
	}
	open := bytes.LastIndexByte(line[:end], '{')
	if open < 0 {
		return 0, false, errors.New("IMAP response has a malformed literal declaration")
	}
	value := line[open+1 : end-1]
	if len(value) > 0 && value[len(value)-1] == '+' {
		value = value[:len(value)-1]
	}
	if len(value) == 0 {
		return 0, false, errors.New("IMAP response has a malformed literal declaration")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false, errors.New("IMAP response has a malformed literal declaration")
		}
	}
	size, err := strconv.ParseUint(string(value), 10, 64)
	if err != nil {
		return 0, false, errors.New("IMAP response has an invalid literal size")
	}
	return size, true, nil
}
