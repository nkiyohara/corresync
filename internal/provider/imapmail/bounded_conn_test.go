package imapmail

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
)

func TestBoundedIMAPConnRejectsOversizedLiteralBeforePayload(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	bounded, err := newBoundedIMAPConn(client, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.WriteString(server, "* 1 FETCH (BODY[] {999999999}\r\n")
	}()
	_, err = io.ReadAll(bounded)
	if err == nil || !strings.Contains(err.Error(), "exceeding") {
		t.Fatalf("ReadAll() error = %v, want literal limit", err)
	}
}

func TestBoundedIMAPConnRejectsBareLFLiteralDeclaration(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	bounded, err := newBoundedIMAPConn(client, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.WriteString(server, "* 1 FETCH (BODY[] {500000000}\n")
	}()
	_, err = io.ReadAll(bounded)
	if err == nil ||
		(!strings.Contains(err.Error(), "exceeding") &&
			!strings.Contains(err.Error(), "CRLF")) {
		t.Fatalf("ReadAll() error = %v, want fail-closed literal parsing", err)
	}
}

func TestBoundedIMAPConnRejectsAggregateLiteralBudget(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	bounded, err := newBoundedIMAPConn(client, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	bounded.maximumTotal = 10
	go func() {
		_, _ = io.WriteString(
			server,
			"* 1 FETCH (BODY[] {6}\r\nfirst!)\r\n"+
				"* 2 FETCH (BODY[] {6}\r\nsecond)\r\n",
		)
	}()
	_, err = io.ReadAll(bounded)
	if err == nil || !strings.Contains(err.Error(), "operation limit") {
		t.Fatalf("ReadAll() error = %v, want aggregate limit", err)
	}
}

func TestBoundedIMAPConnRejectsStatusTextDesynchronization(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	bounded, err := newBoundedIMAPConn(client, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		inner := "* 1 FETCH (BODY[] {9}\r\n"
		payload := inner + strings.Repeat("x", 40-len(inner))
		_, _ = io.WriteString(server, "* OK x {40}\r\n"+payload)
		_ = server.Close()
	}()
	_, err = io.ReadAll(bounded)
	if err == nil || !strings.Contains(err.Error(), "exceeding") {
		t.Fatalf("ReadAll() error = %v, want hidden literal limit", err)
	}
}

func TestBoundedIMAPConnTracksMultipleParserLiterals(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	bounded, err := newBoundedIMAPConn(client, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.WriteString(
			server,
			"* 1 FETCH (BODY[1] {3}\r\nabc BODY[2] {9}\r\n",
		)
	}()
	_, err = io.ReadAll(bounded)
	if err == nil || !strings.Contains(err.Error(), "exceeding") {
		t.Fatalf("ReadAll() error = %v, want second literal limit", err)
	}
}

func TestBoundedIMAPConnParsesLiteralHeavyResponseInOneForwardPass(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	var input strings.Builder
	input.WriteString("* 1 FETCH (")
	input.WriteString("BODY[")
	input.WriteString(strings.Repeat("A", 8<<10))
	input.WriteString("] {1}\r\nx")
	for index := range 200 {
		_, _ = fmt.Fprintf(&input, " BODY[%d] {1}\r\nx", index+1)
	}
	input.WriteString(")\r\n")
	bounded, err := newBoundedIMAPConn(client, 8, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.WriteString(server, input.String())
		_ = server.Close()
	}()
	output, err := io.ReadAll(bounded)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(output) != input.String() {
		t.Fatalf("output length = %d, want %d", len(output), input.Len())
	}
}

func TestBoundedIMAPConnDoesNotParseMarkersInsideLiteralData(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	const literal = "{999}\r\n"
	input := "* 1 FETCH (BODY[] {" + "7" + "}\r\n" + literal + ")\r\n"
	bounded, err := newBoundedIMAPConn(client, len(literal), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = io.WriteString(server, input)
		_ = server.Close()
	}()
	output, err := io.ReadAll(bounded)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(output, []byte(input)) {
		t.Fatalf("output = %q, want %q", output, input)
	}
}

func TestIMAPLiteralSizeHandlesSplitIndependentSyntax(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		line    string
		size    uint64
		literal bool
		wantErr bool
	}{
		{line: "* OK ready\r\n"},
		{line: "* 1 FETCH {42}\r\n", size: 42, literal: true},
		{line: "* 1 FETCH {42}\n", size: 42, literal: true},
		{line: "* 1 FETCH ~{42+}\r\n", size: 42, literal: true},
		{line: "* 1 FETCH {nope}\r\n", wantErr: true},
	} {
		size, literal, err := imapLiteralSize([]byte(test.line))
		if (err != nil) != test.wantErr ||
			size != test.size ||
			literal != test.literal {
			t.Fatalf(
				"imapLiteralSize(%q) = %d, %t, %v",
				test.line,
				size,
				literal,
				err,
			)
		}
	}
}

func TestReadIMAPControlLineRejectsLiteralAndOversizedLine(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"* OK greeting {1}\r\n",
		strings.Repeat("a", maximumIMAPControlLineBytes+1) + "\r\n",
	} {
		_, err := readIMAPControlLine(
			bufioReader(strings.NewReader(input)),
		)
		if err == nil {
			t.Fatalf("readIMAPControlLine(%d bytes) unexpectedly succeeded", len(input))
		}
	}
}

func bufioReader(reader io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(reader, maximumIMAPControlLineBytes+1)
}
