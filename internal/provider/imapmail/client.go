// Package imapmail adapts IMAP4rev1 and SMTP Submission to Corresync's closed
// mail application port.
package imapmail

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	imapclient "github.com/emersion/go-imap/client"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/nkiyohara/corresync/internal/application"
)

const (
	TLSImplicit = "implicit"
	TLSStartTLS = "starttls"

	maximumRawMessageBytes = 8 << 20
	networkTimeout         = 30 * time.Second
)

// Endpoint is one fail-closed TLS endpoint.
type Endpoint struct {
	Host string
	Port uint16
	Mode string
}

// Options identifies one explicitly configured standards mail route. Password
// is copied into mutable client-owned storage and zeroed by Close.
type Options struct {
	IMAP      Endpoint
	SMTP      Endpoint
	Username  string
	Sender    string
	Password  []byte
	TLSConfig *tls.Config
}

// Client opens a fresh authenticated transport for each operation so accounts,
// concurrent calls, rate state, and protocol state cannot bleed together.
type Client struct {
	imap       Endpoint
	smtp       Endpoint
	username   string
	sender     string
	password   []byte
	tlsConfig  *tls.Config
	observed   ObservedCapabilities
	passwordMu sync.RWMutex
	close      sync.Once
}

// ObservedCapabilities are server-advertised IMAP features confirmed after
// authenticated TLS setup. They contain no mailbox data or server banners.
type ObservedCapabilities struct {
	Move    bool
	UIDPlus bool
}

// New validates both TLS transports and authenticates without reading mailbox
// content. It must be called only from an explicit local CLI login.
func New(ctx context.Context, options Options) (*Client, error) {
	if err := validateEndpoint("IMAP", options.IMAP); err != nil {
		return nil, err
	}
	if err := validateEndpoint("SMTP", options.SMTP); err != nil {
		return nil, err
	}
	if options.Username == "" || len(options.Username) > 320 ||
		strings.TrimSpace(options.Username) != options.Username ||
		strings.ContainsAny(options.Username, "\r\n\x00") {
		return nil, errors.New("standards mail username is malformed")
	}
	if len(options.Password) == 0 || len(options.Password) > 64<<10 {
		return nil, errors.New("standards mail credential is empty or too large")
	}
	sender := options.Sender
	if sender == "" {
		sender = options.Username
	}
	if !bareAddress(sender) {
		return nil, errors.New("SMTP sender must be a bare email address")
	}
	tlsConfig, err := secureTLSConfig(options.TLSConfig)
	if err != nil {
		return nil, err
	}
	client := &Client{
		imap: options.IMAP, smtp: options.SMTP,
		username: options.Username, sender: sender,
		password:  append([]byte(nil), options.Password...),
		tlsConfig: tlsConfig,
	}
	if err := client.withIMAP(ctx, func(connection *imapclient.Client) error {
		capabilities, err := connection.Capability()
		if err != nil {
			return err
		}
		client.observed = ObservedCapabilities{
			Move: capabilities["MOVE"], UIDPlus: capabilities["UIDPLUS"],
		}
		return nil
	}); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("authenticate IMAP: %w", err)
	}
	if err := client.withSMTP(ctx, func(*smtp.Client) error { return nil }); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("authenticate SMTP submission: %w", err)
	}
	return client, nil
}

// ObservedCapabilities returns the immutable post-authentication capability
// snapshot collected without reading mailbox content.
func (client *Client) ObservedCapabilities() ObservedCapabilities {
	if client == nil {
		return ObservedCapabilities{}
	}
	return client.observed
}

func (client *Client) withIMAP(
	ctx context.Context,
	operation func(*imapclient.Client) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client.passwordMu.RLock()
	defer client.passwordMu.RUnlock()
	if len(client.password) == 0 {
		return errors.New("standards mail client is closed")
	}
	address := net.JoinHostPort(client.imap.Host, strconv.Itoa(int(client.imap.Port)))
	dialer := &net.Dialer{Timeout: networkTimeout}
	tlsConfig := client.endpointTLS(client.imap.Host)
	var connection *imapclient.Client
	var err error
	switch client.imap.Mode {
	case TLSImplicit:
		connection, err = imapclient.DialWithDialerTLS(dialer, address, tlsConfig)
	case TLSStartTLS:
		connection, err = imapclient.DialWithDialer(dialer, address)
		if err == nil {
			err = connection.StartTLS(tlsConfig)
		}
	default:
		return errors.New("unsupported IMAP TLS mode")
	}
	if err != nil {
		if connection != nil {
			_ = connection.Terminate()
		}
		return err
	}
	connection.Timeout = networkTimeout
	connection.ErrorLog = log.New(io.Discard, "", 0)
	if err := connection.Login(client.username, string(client.password)); err != nil {
		_ = connection.Terminate()
		return err
	}
	operationErr := operation(connection)
	logoutErr := connection.Logout()
	if operationErr != nil {
		return errors.Join(operationErr, logoutErr)
	}
	return nil
}

func (client *Client) withSMTP(
	ctx context.Context,
	operation func(*smtp.Client) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client.passwordMu.RLock()
	defer client.passwordMu.RUnlock()
	if len(client.password) == 0 {
		return errors.New("standards mail client is closed")
	}
	address := net.JoinHostPort(client.smtp.Host, strconv.Itoa(int(client.smtp.Port)))
	tlsConfig := client.endpointTLS(client.smtp.Host)
	var connection *smtp.Client
	var err error
	switch client.smtp.Mode {
	case TLSImplicit:
		connection, err = smtp.DialTLS(address, tlsConfig)
	case TLSStartTLS:
		connection, err = smtp.DialStartTLS(address, tlsConfig)
	default:
		return errors.New("unsupported SMTP TLS mode")
	}
	if err != nil {
		return err
	}
	connection.CommandTimeout = networkTimeout
	connection.SubmissionTimeout = networkTimeout
	if err := connection.Auth(
		sasl.NewPlainClient("", client.username, string(client.password)),
	); err != nil {
		_ = connection.Close()
		return err
	}
	operationErr := operation(connection)
	_ = connection.Quit()
	return operationErr
}

func (client *Client) endpointTLS(serverName string) *tls.Config {
	copy := client.tlsConfig.Clone()
	copy.ServerName = serverName
	return copy
}

// Close overwrites the credential bytes owned by the adapter.
func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	client.close.Do(func() {
		client.passwordMu.Lock()
		defer client.passwordMu.Unlock()
		for index := range client.password {
			client.password[index] = 0
		}
		client.password = nil
	})
	return nil
}

func secureTLSConfig(input *tls.Config) (*tls.Config, error) {
	var result *tls.Config
	if input == nil {
		result = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		result = input.Clone()
		if result.InsecureSkipVerify {
			return nil, errors.New("TLS certificate verification cannot be disabled")
		}
		if result.MinVersion == 0 || result.MinVersion < tls.VersionTLS12 {
			result.MinVersion = tls.VersionTLS12
		}
	}
	return result, nil
}

func validateEndpoint(name string, endpoint Endpoint) error {
	if endpoint.Host == "" || len(endpoint.Host) > 253 ||
		strings.TrimSpace(endpoint.Host) != endpoint.Host ||
		strings.ContainsAny(endpoint.Host, "\r\n\x00/@") {
		return fmt.Errorf("%s host is malformed", name)
	}
	if endpoint.Port == 0 {
		return fmt.Errorf("%s port is required", name)
	}
	switch endpoint.Mode {
	case TLSImplicit, TLSStartTLS:
	default:
		return fmt.Errorf("%s TLS mode must be implicit or starttls", name)
	}
	return nil
}

func bareAddress(value string) bool {
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Name == "" && parsed.Address == value &&
		strings.Contains(value, "@")
}

type messageReference struct {
	Mailbox     string `json:"mailbox"`
	UIDValidity uint32 `json:"uidValidity"`
	UID         uint32 `json:"uid"`
}

func encodeMessageID(reference messageReference) (string, error) {
	data, err := json.Marshal(reference)
	if err != nil {
		return "", err
	}
	return "ima1_" + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeMessageID(value string) (messageReference, error) {
	if !strings.HasPrefix(value, "ima1_") {
		return messageReference{}, errors.New("message ID is not an IMAP identifier")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "ima1_"))
	if err != nil || len(data) > 2048 {
		return messageReference{}, errors.New("IMAP message ID is malformed")
	}
	var reference messageReference
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil ||
		reference.Mailbox == "" || reference.UIDValidity == 0 || reference.UID == 0 {
		return messageReference{}, errors.New("IMAP message ID is malformed")
	}
	return reference, nil
}

func encodeFolderID(mailbox string) string {
	return "imf1_" + base64.RawURLEncoding.EncodeToString([]byte(mailbox))
}

func decodeFolderID(value string) (string, error) {
	if !strings.HasPrefix(value, "imf1_") {
		return "", errors.New("folder ID is not an IMAP identifier")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "imf1_"))
	if err != nil || len(data) == 0 || len(data) > 1024 ||
		strings.ContainsAny(string(data), "\r\n\x00") {
		return "", errors.New("IMAP folder ID is malformed")
	}
	return string(data), nil
}

func snapshot(status *imap.MailboxStatus, message *imap.Message) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(
		hash,
		"%d\x00%d\x00%d\x00%d\x00%s\x00",
		status.UidValidity,
		message.Uid,
		message.Size,
		message.InternalDate.UnixNano(),
		strings.Join(sortedStrings(message.Flags), "\x00"),
	)
	if message.Envelope != nil {
		_, _ = fmt.Fprintf(
			hash,
			"%s\x00%s\x00%s",
			message.Envelope.Subject,
			message.Envelope.MessageId,
			message.Envelope.InReplyTo,
		)
	}
	return "imc1_" + hex.EncodeToString(hash.Sum(nil))
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func hasFlag(flags []string, expected string) bool {
	for _, flag := range flags {
		if strings.EqualFold(flag, expected) {
			return true
		}
	}
	return false
}

func (client *Client) resolveMailbox(
	connection *imapclient.Client,
	folder application.MailFolder,
) (string, error) {
	if folder.Kind == application.MailFolderOpaque {
		return decodeFolderID(folder.ID)
	}
	infos, err := listMailboxes(connection)
	if err != nil {
		return "", err
	}
	role := strings.ToLower(folder.ID)
	for _, info := range infos {
		if role == "msgfolderroot" {
			return "", nil
		}
		if role == "inbox" && strings.EqualFold(info.Name, imap.InboxName) {
			return info.Name, nil
		}
		attribute := map[string]string{
			"archive":      imap.ArchiveAttr,
			"deleteditems": imap.TrashAttr,
			"drafts":       imap.DraftsAttr,
			"sentitems":    imap.SentAttr,
		}[role]
		if attribute != "" && hasFlag(info.Attributes, attribute) {
			return info.Name, nil
		}
	}
	fallback := map[string]string{
		"archive": "Archive", "deleteditems": "Trash",
		"drafts": "Drafts", "sentitems": "Sent",
	}[role]
	for _, info := range infos {
		if fallback != "" && strings.EqualFold(info.Name, fallback) {
			return info.Name, nil
		}
	}
	return "", fmt.Errorf("IMAP has no mailbox for distinguished folder %q", folder.ID)
}

func listMailboxes(connection *imapclient.Client) ([]*imap.MailboxInfo, error) {
	mailboxes := make(chan *imap.MailboxInfo, 32)
	done := make(chan error, 1)
	go func() { done <- connection.List("", "*", mailboxes) }()
	result := make([]*imap.MailboxInfo, 0, 32)
	for mailbox := range mailboxes {
		result = append(result, mailbox)
		if len(result) > 1024 {
			return nil, errors.New("IMAP mailbox count exceeds the limit")
		}
	}
	if err := <-done; err != nil {
		return nil, err
	}
	return result, nil
}
