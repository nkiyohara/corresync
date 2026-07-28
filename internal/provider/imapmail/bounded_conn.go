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

	imapclient "github.com/emersion/go-imap/client"
)

const (
	maximumIMAPControlLineBytes = 64 << 10
	maximumIMAPOperationBytes   = 32 << 20
	maximumIMAPUpgradeLines     = 100
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

	readMu           sync.Mutex
	pending          []byte
	controlLine      []byte
	literalRemaining uint64
	terminalErr      error
	errorReturned    bool
	maximumTotal     uint64
	literalTotal     uint64
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
		controlLine:    make([]byte, 0, 4096),
	}, nil
}

func (connection *boundedIMAPConn) Read(destination []byte) (int, error) {
	connection.readMu.Lock()
	defer connection.readMu.Unlock()
	if len(destination) == 0 {
		return 0, nil
	}
	for len(connection.pending) == 0 && connection.terminalErr == nil {
		buffer := make([]byte, 32<<10)
		count, readErr := connection.Conn.Read(buffer)
		if count > 0 {
			if err := connection.inspect(buffer[:count]); err != nil {
				connection.terminalErr = err
				_ = connection.Close()
			}
		}
		if readErr != nil && connection.terminalErr == nil {
			if connection.literalRemaining != 0 {
				connection.terminalErr = io.ErrUnexpectedEOF
			} else {
				connection.terminalErr = readErr
			}
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

func (connection *boundedIMAPConn) inspect(input []byte) error {
	for len(input) > 0 {
		if connection.literalRemaining > 0 {
			count := min(uint64(len(input)), connection.literalRemaining)
			connection.pending = append(connection.pending, input[:count]...)
			connection.literalRemaining -= count
			input = input[count:]
			continue
		}
		character := input[0]
		input = input[1:]
		if len(connection.controlLine) == maximumIMAPControlLineBytes {
			return errors.New("IMAP response control line exceeds its size bound")
		}
		connection.controlLine = append(connection.controlLine, character)
		if character != '\n' {
			connection.pending = append(connection.pending, character)
			continue
		}
		size, literal, err := imapLiteralSize(connection.controlLine)
		if err != nil {
			return err
		}
		if literal && size > connection.maximumLiteral {
			return fmt.Errorf(
				"IMAP literal declares %d bytes, exceeding the %d-byte limit",
				size,
				connection.maximumLiteral,
			)
		}
		if len(connection.controlLine) < 2 ||
			connection.controlLine[len(connection.controlLine)-2] != '\r' {
			return errors.New("IMAP response control line does not use CRLF")
		}
		if literal &&
			(size > connection.maximumTotal ||
				connection.literalTotal > connection.maximumTotal-size) {
			return fmt.Errorf(
				"IMAP response literals exceed the %d-byte operation limit",
				connection.maximumTotal,
			)
		}
		connection.pending = append(connection.pending, character)
		connection.controlLine = connection.controlLine[:0]
		if literal {
			connection.literalTotal += size
			connection.literalRemaining = size
		}
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
