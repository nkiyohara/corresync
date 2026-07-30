package imapmail

import (
	"errors"
	"strings"
)

const xoauth2Mechanism = "XOAUTH2"

// xoauth2Client implements Google's SASL XOAUTH2 exchange for IMAP and SMTP.
// It deliberately retains only mutable token-bearing byte slices so Close can
// erase the adapter-owned copies after each authentication attempt.
type xoauth2Client struct {
	username string
	token    []byte
	initial  []byte
	started  bool
	replied  bool
}

func newXOAuth2Client(username string, token []byte) *xoauth2Client {
	return &xoauth2Client{
		username: username,
		token:    append([]byte(nil), token...),
	}
}

func (client *xoauth2Client) Start() (string, []byte, error) {
	if client.started {
		return "", nil, errors.New("XOAUTH2 exchange already started")
	}
	client.started = true
	if client.username == "" ||
		strings.ContainsAny(client.username, "\r\n\x00\x01") ||
		len(client.token) == 0 {
		return "", nil, errors.New("XOAUTH2 credentials are malformed")
	}
	client.initial = make(
		[]byte,
		0,
		len("user=")+len(client.username)+len("\x01auth=Bearer \x01\x01")+len(client.token),
	)
	client.initial = append(client.initial, "user="...)
	client.initial = append(client.initial, client.username...)
	client.initial = append(client.initial, '\x01')
	client.initial = append(client.initial, "auth=Bearer "...)
	client.initial = append(client.initial, client.token...)
	client.initial = append(client.initial, '\x01', '\x01')
	return xoauth2Mechanism, client.initial, nil
}

func (client *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	if !client.started {
		return nil, errors.New("XOAUTH2 exchange has not started")
	}
	if client.replied {
		return nil, errors.New("XOAUTH2 server sent more than one challenge")
	}
	client.replied = true
	if len(challenge) > maximumAccessTokenBytes {
		return nil, errors.New("XOAUTH2 server challenge is too large")
	}
	if len(challenge) == 0 {
		return nil, errors.New("XOAUTH2 server sent an empty challenge")
	}
	// Gmail returns a JSON error challenge and requires one empty response
	// before completing the SASL failure. The remote status remains the
	// authoritative error and is intentionally not logged with token material.
	return []byte{}, nil
}

func (client *xoauth2Client) Close() error {
	if client == nil {
		return nil
	}
	erase(client.token)
	erase(client.initial)
	client.token = nil
	client.initial = nil
	return nil
}
