package imapmail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/backend/memory"
	imapserver "github.com/emersion/go-imap/server"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/nkiyohara/corresync/internal/application"
)

type syntheticSMTPBackend struct {
	mu          sync.Mutex
	messages    [][]byte
	recipients  [][]string
	afterAccept func([]byte) error
}

func (backend *syntheticSMTPBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &syntheticSMTPSession{backend: backend}, nil
}

type syntheticSMTPSession struct {
	backend    *syntheticSMTPBackend
	authorized bool
	recipients []string
}

func (*syntheticSMTPSession) AuthMechanisms() []string {
	return []string{sasl.Plain, xoauth2Mechanism}
}

func (session *syntheticSMTPSession) Auth(mechanism string) (sasl.Server, error) {
	switch mechanism {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, username, password string) error {
			if username != "username" || password != "password" {
				return errors.New("invalid synthetic credentials")
			}
			session.authorized = true
			return nil
		}), nil
	case xoauth2Mechanism:
		return &syntheticXOAuth2Server{authenticate: func(username, token string) error {
			if username != "username" || token != "synthetic-access-token" {
				return errors.New("invalid synthetic OAuth2 credentials")
			}
			session.authorized = true
			return nil
		}}, nil
	default:
		return nil, errors.New("unsupported synthetic authentication mechanism")
	}
}

func (session *syntheticSMTPSession) Mail(
	from string,
	_ *smtp.MailOptions,
) error {
	if !session.authorized {
		return smtp.ErrAuthRequired
	}
	if from != "reader@example.invalid" {
		return errors.New("unexpected synthetic sender")
	}
	return nil
}

func (session *syntheticSMTPSession) Rcpt(
	recipient string,
	_ *smtp.RcptOptions,
) error {
	if !session.authorized {
		return smtp.ErrAuthRequired
	}
	session.recipients = append(session.recipients, recipient)
	return nil
}

func (session *syntheticSMTPSession) Data(reader io.Reader) error {
	if !session.authorized {
		return smtp.ErrAuthRequired
	}
	message, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	session.backend.mu.Lock()
	session.backend.messages = append(session.backend.messages, message)
	session.backend.recipients = append(
		session.backend.recipients,
		append([]string(nil), session.recipients...),
	)
	afterAccept := session.backend.afterAccept
	session.backend.mu.Unlock()
	if afterAccept != nil {
		return afterAccept(message)
	}
	return nil
}

func (*syntheticSMTPSession) Reset()        {}
func (*syntheticSMTPSession) Logout() error { return nil }

type standardsFixture struct {
	imap      Endpoint
	smtp      Endpoint
	tls       *tls.Config
	out       *syntheticSMTPBackend
	storeSent func([]byte) error
}

func newStandardsFixture(t *testing.T) standardsFixture {
	return newStandardsFixtureWithUIDPlus(t, false)
}

func newStandardsFixtureWithUIDPlus(t *testing.T, uidPlus bool) standardsFixture {
	t.Helper()
	serverTLS, clientTLS := syntheticTLS(t)

	imapBackend := memory.New()
	user, err := imapBackend.Login(
		&imap.ConnInfo{},
		"username",
		"password",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, mailbox := range []string{"Drafts", "Sent", "Trash", "Archive"} {
		if err := user.CreateMailbox(mailbox); err != nil {
			t.Fatal(err)
		}
	}
	imapListener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	imapService := imapserver.New(imapBackend)
	if uidPlus {
		imapService.Enable(syntheticUIDPlusExtension{})
	}
	imapService.EnableAuth(
		xoauth2Mechanism,
		func(connection imapserver.Conn) sasl.Server {
			return &syntheticXOAuth2Server{authenticate: func(username, token string) error {
				if username != "username" || token != "synthetic-access-token" {
					return errors.New("invalid synthetic OAuth2 credentials")
				}
				user, err := imapBackend.Login(
					connection.Info(),
					"username",
					"password",
				)
				if err != nil {
					return err
				}
				connection.Context().State = imap.AuthenticatedState
				connection.Context().User = user
				return nil
			}}
		},
	)
	imapService.TLSConfig = serverTLS
	imapService.ErrorLog = log.New(io.Discard, "", 0)
	go func() { _ = imapService.Serve(imapListener) }()
	t.Cleanup(func() { _ = imapService.Close() })

	smtpBackend := new(syntheticSMTPBackend)
	smtpListener, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatal(err)
	}
	smtpService := smtp.NewServer(smtpBackend)
	smtpService.Domain = "example.invalid"
	smtpService.TLSConfig = serverTLS
	smtpService.ErrorLog = log.New(io.Discard, "", 0)
	smtpService.ReadTimeout = 5 * time.Second
	smtpService.WriteTimeout = 5 * time.Second
	go func() { _ = smtpService.Serve(smtpListener) }()
	t.Cleanup(func() { _ = smtpService.Close() })

	imapHost, imapPort := splitListener(t, imapListener)
	smtpHost, smtpPort := splitListener(t, smtpListener)
	return standardsFixture{
		imap: Endpoint{Host: imapHost, Port: imapPort, Mode: TLSImplicit},
		smtp: Endpoint{Host: smtpHost, Port: smtpPort, Mode: TLSImplicit},
		tls:  clientTLS,
		out:  smtpBackend,
		storeSent: func(message []byte) error {
			mailbox, mailboxErr := user.GetMailbox("Sent")
			if mailboxErr != nil {
				return mailboxErr
			}
			return mailbox.CreateMessage(
				[]string{imap.SeenFlag},
				time.Now(),
				bytes.NewReader(message),
			)
		},
	}
}

type syntheticUIDPlusExtension struct{}

func (syntheticUIDPlusExtension) Capabilities(imapserver.Conn) []string {
	return []string{"UIDPLUS"}
}

func (syntheticUIDPlusExtension) Command(name string) imapserver.HandlerFactory {
	if name != "EXPUNGE" {
		return nil
	}
	return func() imapserver.Handler { return new(syntheticUIDExpunge) }
}

type syntheticUIDExpunge struct {
	set *imap.SeqSet
}

func (command *syntheticUIDExpunge) Parse(fields []interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	if len(fields) != 1 {
		return errors.New("synthetic UID EXPUNGE has an invalid argument count")
	}
	raw, ok := fields[0].(string)
	if !ok {
		return errors.New("synthetic UID EXPUNGE has an invalid sequence set")
	}
	set, err := imap.ParseSeqSet(raw)
	if err != nil {
		return err
	}
	command.set = set
	return nil
}

func (command *syntheticUIDExpunge) Handle(connection imapserver.Conn) error {
	if command.set != nil {
		return errors.New("synthetic sequence set requires UID EXPUNGE")
	}
	if connection.Context().Mailbox == nil {
		return errors.New("no synthetic mailbox is selected")
	}
	return connection.Context().Mailbox.Expunge()
}

func (command *syntheticUIDExpunge) UidHandle(connection imapserver.Conn) error {
	mailbox := connection.Context().Mailbox
	if mailbox == nil || command.set == nil {
		return errors.New("synthetic UID EXPUNGE lacks a selected target")
	}
	criteria := imap.NewSearchCriteria()
	criteria.WithFlags = []string{imap.DeletedFlag}
	deleted, err := mailbox.SearchMessages(true, criteria)
	if err != nil {
		return err
	}
	preserved := new(imap.SeqSet)
	for _, uid := range deleted {
		if !command.set.Contains(uid) {
			preserved.AddNum(uid)
		}
	}
	if len(preserved.Set) != 0 {
		if err := mailbox.UpdateMessagesFlags(
			true, preserved, imap.RemoveFlags, []string{imap.DeletedFlag},
		); err != nil {
			return err
		}
	}
	if err := mailbox.Expunge(); err != nil {
		return err
	}
	if len(preserved.Set) != 0 {
		return mailbox.UpdateMessagesFlags(
			true, preserved, imap.AddFlags, []string{imap.DeletedFlag},
		)
	}
	return nil
}

type syntheticXOAuth2Server struct {
	authenticate func(username, token string) error
	complete     bool
}

func TestXOAuth2ClientBoundsChallengesAndErasesOwnedCredentials(t *testing.T) {
	token := []byte("synthetic-secret")
	client := newXOAuth2Client("reader@example.invalid", token)
	mechanism, initial, err := client.Start()
	if err != nil {
		t.Fatal(err)
	}
	if mechanism != xoauth2Mechanism ||
		string(initial) != "user=reader@example.invalid\x01auth=Bearer synthetic-secret\x01\x01" {
		t.Fatalf("XOAUTH2 initial response = %q %q", mechanism, initial)
	}
	if _, err := client.Next(make([]byte, maximumAccessTokenBytes+1)); err == nil ||
		!strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversized XOAUTH2 challenge error = %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(initial, make([]byte, len(initial))) {
		t.Fatalf("XOAUTH2 initial response was not erased: %q", initial)
	}
	if string(token) != "synthetic-secret" {
		t.Fatalf("XOAUTH2 constructor modified caller token: %q", token)
	}
}

func (server *syntheticXOAuth2Server) Next(
	response []byte,
) ([]byte, bool, error) {
	if server.complete {
		return nil, false, errors.New("synthetic XOAUTH2 exchange already completed")
	}
	server.complete = true
	const userPrefix = "user="
	// #nosec G101 -- this is the fixed XOAUTH2 field label, not a credential.
	const bearerMarker = "\x01auth=Bearer "
	const suffix = "\x01\x01"
	value := string(response)
	marker := strings.Index(value, bearerMarker)
	if !strings.HasPrefix(value, userPrefix) ||
		marker <= len(userPrefix) ||
		!strings.HasSuffix(value, suffix) {
		return nil, false, errors.New("malformed synthetic XOAUTH2 response")
	}
	username := value[len(userPrefix):marker]
	token := value[marker+len(bearerMarker) : len(value)-len(suffix)]
	if server.authenticate == nil {
		return nil, false, errors.New("synthetic XOAUTH2 authenticator is missing")
	}
	if err := server.authenticate(username, token); err != nil {
		return nil, false, err
	}
	return nil, true, nil
}

type syntheticAccessTokenSource struct {
	mu     sync.Mutex
	issued [][]byte
}

func (source *syntheticAccessTokenSource) AccessToken(
	ctx context.Context,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	token := []byte("synthetic-access-token")
	source.issued = append(source.issued, token)
	return token, nil
}

func TestClientAuthenticatesIMAPAndSMTPWithFreshXOAUTH2Tokens(t *testing.T) {
	fixture := newStandardsFixture(t)
	source := new(syntheticAccessTokenSource)
	client, err := New(t.Context(), Options{
		IMAP: fixture.imap, SMTP: fixture.smtp,
		Username: "username", Sender: "reader@example.invalid",
		OAuth2: source, TLSConfig: fixture.tls,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 1,
	}); err != nil {
		t.Fatal(err)
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if len(source.issued) != 3 {
		t.Fatalf("OAuth2 token requests = %d, want IMAP + SMTP + IMAP", len(source.issued))
	}
	for index, token := range source.issued {
		if !bytes.Equal(token, make([]byte, len(token))) {
			t.Fatalf("issued OAuth2 token %d was not erased", index)
		}
	}
}

func TestClientConfirmsSMTPStoredSentCopyWithoutAppendingDuplicate(t *testing.T) {
	fixture := newStandardsFixture(t)
	fixture.out.mu.Lock()
	fixture.out.afterAccept = fixture.storeSent
	fixture.out.mu.Unlock()
	client, err := New(t.Context(), Options{
		IMAP: fixture.imap, SMTP: fixture.smtp,
		Username: "username", Sender: "reader@example.invalid",
		OAuth2:         new(syntheticAccessTokenSource),
		SMTPStoresSent: true,
		TLSConfig:      fixture.tls,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	sent, err := client.SendMail(t.Context(), application.MailSendInput{
		Account: "acc_00000000000000000000000000000001",
		To:      []string{"recipient@example.invalid"},
		Subject: "Server-stored submission",
		Body:    "The SMTP server owns the Sent copy.",
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished,
			ID:   "sentitems",
		},
		Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 ||
		page.Messages[0].ID != sent.ID ||
		page.Messages[0].Subject != "Server-stored submission" {
		t.Fatalf("server-stored Sent page = %#v, result = %#v", page, sent)
	}
}

func TestClientSendsAndRemovesOneExactSavedDraft(t *testing.T) {
	fixture := newStandardsFixtureWithUIDPlus(t, true)
	client, err := New(t.Context(), Options{
		IMAP: fixture.imap, SMTP: fixture.smtp,
		Username: "username", Sender: "reader@example.invalid",
		Password: []byte("password"), TLSConfig: fixture.tls,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	observed := client.ObservedCapabilities()
	if !observed.UIDPlus || !observed.Drafts || !observed.Sent {
		t.Fatalf("observed capabilities = %#v", observed)
	}
	draft, err := client.CreateMailDraft(t.Context(), application.MailDraftInput{
		Account: "work",
		To:      []string{"to@example.invalid"},
		CC:      []string{"cc@example.invalid"},
		BCC:     []string{"bcc@example.invalid"},
		Subject: "Reviewed saved draft",
		Body:    "Send these exact saved bytes.",
		Attachments: []application.MailFileAttachment{{
			Name: "fixture.txt", ContentType: "text/plain", Content: []byte("fixture"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := application.MailDraftSendInput{
		Account: "work", DraftID: draft.ID, DraftChangeKey: draft.ChangeKey,
	}
	snapshot, err := client.GetMailDraftSnapshot(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Validate(input, application.MaxMailRecipients); err != nil {
		t.Fatalf("snapshot validation = %v; snapshot=%+v", err, snapshot)
	}
	if !slices.Equal(snapshot.To, []string{"to@example.invalid"}) ||
		!slices.Equal(snapshot.CC, []string{"cc@example.invalid"}) ||
		!slices.Equal(snapshot.BCC, []string{"bcc@example.invalid"}) ||
		snapshot.Subject != "Reviewed saved draft" || len(snapshot.Attachments) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	sent, err := client.SendMailDraft(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if sent.ID == "" || sent.ChangeKey == "" {
		t.Fatalf("sent result = %+v", sent)
	}
	drafts, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "drafts"},
		Limit:  25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts.Messages) != 0 {
		t.Fatalf("Drafts after send = %+v", drafts)
	}
	sentPage, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{Kind: application.MailFolderDistinguished, ID: "sentitems"},
		Limit:  25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sentPage.Messages) != 1 || sentPage.Messages[0].ID != sent.ID {
		t.Fatalf("Sent after send = %+v; result=%+v", sentPage, sent)
	}
	fixture.out.mu.Lock()
	defer fixture.out.mu.Unlock()
	if len(fixture.out.messages) != 1 || len(fixture.out.recipients) != 1 ||
		!slices.Equal(fixture.out.recipients[0], []string{
			"to@example.invalid", "cc@example.invalid", "bcc@example.invalid",
		}) || bytes.Contains(fixture.out.messages[0], []byte("\r\nBcc:")) ||
		!bytes.Contains(fixture.out.messages[0], []byte("Reviewed saved draft")) ||
		!bytes.Contains(fixture.out.messages[0], []byte("fixture.txt")) {
		t.Fatalf(
			"SMTP capture messages=%q recipients=%#v",
			fixture.out.messages,
			fixture.out.recipients,
		)
	}
}

func TestClientRejectsExactDraftSendWithoutSafeRemovalBeforeSMTP(t *testing.T) {
	fixture := newStandardsFixture(t)
	client, err := New(t.Context(), Options{
		IMAP: fixture.imap, SMTP: fixture.smtp,
		Username: "username", Sender: "reader@example.invalid",
		Password: []byte("password"), TLSConfig: fixture.tls,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	input := application.MailDraftSendInput{
		Account: "work", DraftID: "not-read", DraftChangeKey: "not-read",
	}
	_, err = client.GetMailDraftSnapshot(t.Context(), input)
	if !errors.Is(err, application.ErrExactDraftSendUnavailable) {
		t.Fatalf("GetMailDraftSnapshot() error = %v", err)
	}
	_, err = client.SendMailDraft(t.Context(), input)
	if !errors.Is(err, application.ErrExactDraftSendUnavailable) {
		t.Fatalf("SendMailDraft() error = %v", err)
	}
	fixture.out.mu.Lock()
	defer fixture.out.mu.Unlock()
	if len(fixture.out.messages) != 0 {
		t.Fatalf("SMTP was attempted before capability rejection: %q", fixture.out.messages)
	}
}

func TestDraftHeaderAddressesRejectDuplicateRecipientFields(t *testing.T) {
	t.Parallel()
	_, err := draftHeaderAddresses(mail.Header{
		"To": []string{"first@example.invalid", "second@example.invalid"},
	}, "To")
	if err == nil {
		t.Fatal("draftHeaderAddresses() accepted duplicate To headers")
	}
}

func TestClientDisablesPermanentDeleteBeforeParsingOrNetwork(t *testing.T) {
	client := &Client{disablePermanentDelete: true}
	err := client.DeleteMail(t.Context(), application.MailDeleteInput{
		MessageID: "not-an-imap-identifier",
		ChangeKey: "not-an-imap-snapshot",
	})
	if err == nil || !strings.Contains(err.Error(), "permanent deletion is unavailable") {
		t.Fatalf("disabled permanent delete error = %v", err)
	}
}

func syntheticTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	serverTLS := server.TLS.Clone()
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("synthetic HTTPS transport has unexpected type")
	}
	clientTLS := transport.TLSClientConfig.Clone()
	server.Close()
	return serverTLS, clientTLS
}

func splitListener(t *testing.T, listener net.Listener) (string, uint16) {
	t.Helper()
	host, rawPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	return host, uint16(port)
}

func TestClientReadsConditionallyUpdatesAndSubmitsSyntheticStandardsMail(t *testing.T) {
	fixture := newStandardsFixture(t)
	secret := []byte("password")
	client, err := New(t.Context(), Options{
		IMAP: fixture.imap, SMTP: fixture.smtp,
		Username: "username", Sender: "reader@example.invalid",
		Password: secret, TLSConfig: fixture.tls,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for index := range secret {
		secret[index] = 'x'
	}
	observed := client.ObservedCapabilities()
	if !observed.Move || observed.UIDPlus || !observed.Sent {
		t.Fatalf("synthetic IMAP capabilities = %#v", observed)
	}

	page, err := client.ListMessages(t.Context(), application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 ||
		page.Messages[0].Subject != "A little message, just for you" ||
		page.Messages[0].ChangeKey == "" {
		t.Fatalf("mail page = %#v", page)
	}
	body, err := client.GetMessageBody(t.Context(), application.MailBodyInput{
		MessageID: page.Messages[0].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if body.Text != "Hi there :)" {
		t.Fatalf("mail body = %#v", body)
	}
	updated, err := client.SetMailReadState(
		t.Context(),
		application.MailReadStateInput{
			MessageID: page.Messages[0].ID,
			ChangeKey: page.Messages[0].ChangeKey,
			State:     application.MailReadStateUnread,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ChangeKey == page.Messages[0].ChangeKey {
		t.Fatal("read-state update did not produce a new snapshot")
	}
	_, err = client.SetMailReadState(
		t.Context(),
		application.MailReadStateInput{
			MessageID: page.Messages[0].ID,
			ChangeKey: page.Messages[0].ChangeKey,
			State:     application.MailReadStateRead,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale update error = %v", err)
	}

	sent, err := client.SendMail(t.Context(), application.MailSendInput{
		Account: "acc_00000000000000000000000000000001",
		To:      []string{"recipient@example.invalid"},
		Subject: "Synthetic submission",
		Body:    "Hello over SMTP",
		Attachments: []application.MailFileAttachment{{
			Name: "fixture.txt", ContentType: "text/plain",
			Content: []byte("fixture"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sent.ID, "ima1_") || sent.ChangeKey == "" {
		t.Fatalf("send result = %#v", sent)
	}
	sentPage, err := client.ListMessages(
		t.Context(),
		application.MailListInput{
			Folder: application.MailFolder{
				Kind: application.MailFolderDistinguished,
				ID:   "sentitems",
			},
			Limit: 25,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(sentPage.Messages) != 1 ||
		sentPage.Messages[0].ID != sent.ID ||
		sentPage.Messages[0].Subject != "Synthetic submission" ||
		!sentPage.Messages[0].IsRead {
		t.Fatalf("IMAP Sent page = %#v", sentPage)
	}
	fixture.out.mu.Lock()
	defer fixture.out.mu.Unlock()
	if len(fixture.out.messages) != 1 ||
		!bytes.Contains(fixture.out.messages[0], []byte("Synthetic submission")) ||
		!bytes.Contains(fixture.out.messages[0], []byte("fixture.txt")) ||
		!bytes.Contains(fixture.out.messages[0], []byte("Zml4dHVyZQ==")) ||
		len(fixture.out.recipients) != 1 ||
		len(fixture.out.recipients[0]) != 1 ||
		fixture.out.recipients[0][0] != "recipient@example.invalid" {
		t.Fatalf(
			"SMTP capture messages=%q recipients=%#v",
			fixture.out.messages,
			fixture.out.recipients,
		)
	}
}

func TestClientOwnsCredentialAndRejectsTLSVerificationBypass(t *testing.T) {
	fixture := newStandardsFixture(t)
	secret := []byte("password")
	client, err := New(t.Context(), Options{
		IMAP: fixture.imap, SMTP: fixture.smtp,
		Username: "username", Sender: "reader@example.invalid",
		Password: secret, TLSConfig: fixture.tls,
	})
	if err != nil {
		t.Fatal(err)
	}
	owned := client.password
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(owned, make([]byte, len(owned))) ||
		!bytes.Equal(secret, []byte("password")) {
		t.Fatal("credential ownership or zeroing is incorrect")
	}

	unsafe := fixture.tls.Clone()
	unsafe.InsecureSkipVerify = true
	_, err = New(t.Context(), Options{
		IMAP: fixture.imap, SMTP: fixture.smtp,
		Username: "username", Sender: "reader@example.invalid",
		Password: []byte("password"), TLSConfig: unsafe,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be disabled") {
		t.Fatalf("unsafe TLS error = %v", err)
	}
}

func TestSendFailsBeforeSMTPWhenSentMailboxWasNotDiscovered(t *testing.T) {
	t.Parallel()

	client := &Client{sender: "reader@example.invalid"}
	_, err := client.SendMail(
		t.Context(),
		application.MailSendInput{
			To:      []string{"recipient@example.invalid"},
			Subject: "Must not send",
			Body:    "The IMAP account has no Sent mailbox.",
		},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "SMTP submission was not attempted") {
		t.Fatalf("SendMail() error = %v", err)
	}
}

func TestIMAPCommittedWriteErrorsRequireReconciliation(t *testing.T) {
	t.Parallel()
	for _, source := range []error{
		errors.New("synthetic confirmation failure"),
		fmt.Errorf(
			"%w: synthetic transport failure",
			application.ErrWriteOutcomeUnknown,
		),
	} {
		err := imapCommittedWriteError("confirm synthetic write", source)
		if !errors.Is(err, application.ErrWriteOutcomeUnknown) ||
			!errors.Is(err, source) {
			t.Fatalf("imapCommittedWriteError() = %v", err)
		}
	}
}

func TestDialBoundedIMAPCompletesSTARTTLSBeforeLibraryParsing(t *testing.T) {
	t.Parallel()
	serverTLS, clientTLS := syntheticTLS(t)
	backend := memory.New()
	if _, err := backend.Login(
		&imap.ConnInfo{},
		"username",
		"password",
	); err != nil {
		t.Fatal(err)
	}
	listener, err := (&net.ListenConfig{}).Listen(
		t.Context(),
		"tcp",
		"127.0.0.1:0",
	)
	if err != nil {
		t.Fatal(err)
	}
	service := imapserver.New(backend)
	service.TLSConfig = serverTLS
	service.ErrorLog = log.New(io.Discard, "", 0)
	go func() { _ = service.Serve(listener) }()
	t.Cleanup(func() { _ = service.Close() })
	clientTLS.ServerName = "example.com"

	connection, err := dialBoundedIMAP(
		t.Context(),
		&net.Dialer{Timeout: networkTimeout},
		listener.Addr().String(),
		TLSStartTLS,
		clientTLS,
	)
	if err != nil {
		t.Fatalf("dialBoundedIMAP() error = %v", err)
	}
	t.Cleanup(func() { _ = connection.Terminate() })
	connection.Timeout = networkTimeout
	if err := connection.Login("username", "password"); err != nil {
		t.Fatalf("Login() after STARTTLS error = %v", err)
	}
	if _, err := connection.Capability(); err != nil {
		t.Fatalf("Capability() after STARTTLS error = %v", err)
	}
	if err := connection.Logout(); err != nil {
		t.Fatalf("Logout() after STARTTLS error = %v", err)
	}
}

func TestIMAPCollectorsDrainHostileExcessResponses(t *testing.T) {
	t.Parallel()
	messages, err := collectFetchedMessages(
		t.Context(),
		1,
		func(output chan *imap.Message) error {
			defer close(output)
			output <- &imap.Message{Uid: 1}
			output <- &imap.Message{Uid: 1}
			return nil
		},
	)
	if err == nil || len(messages) != 0 {
		t.Fatalf("collectFetchedMessages() = %d, %v; want bounded error", len(messages), err)
	}

	mailboxes, err := collectMailboxes(
		t.Context(),
		func(output chan *imap.MailboxInfo) error {
			defer close(output)
			for index := range 1025 {
				output <- &imap.MailboxInfo{Name: strconv.Itoa(index)}
			}
			return nil
		},
	)
	if err == nil || len(mailboxes) != 0 {
		t.Fatalf("collectMailboxes() = %d, %v; want bounded error", len(mailboxes), err)
	}
}

func TestMailboxSelectableRejectsNoSelectContainers(t *testing.T) {
	t.Parallel()
	if mailboxSelectable(nil) {
		t.Fatal("mailboxSelectable(nil) = true")
	}
	if mailboxSelectable(&imap.MailboxInfo{
		Name:       "[Gmail]",
		Attributes: []string{imap.NoSelectAttr},
	}) {
		t.Fatal("mailboxSelectable() accepted a noselect container")
	}
	if !mailboxSelectable(&imap.MailboxInfo{Name: imap.InboxName}) {
		t.Fatal("mailboxSelectable() rejected a selectable mailbox")
	}
}

func TestParseMIMEKeepsAttachmentsBoundedAndAddressable(t *testing.T) {
	t.Parallel()
	raw := "From: sender@example.invalid\r\n" +
		"To: reader@example.invalid\r\n" +
		"Subject: multipart\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=fixture\r\n\r\n" +
		"--fixture\r\nContent-Type: text/plain\r\n\r\nHello\r\n" +
		"--fixture\r\nContent-Type: text/plain\r\n" +
		"Content-Disposition: attachment; filename=report.txt\r\n\r\nfixture\r\n" +
		"--fixture--\r\n"
	parsed, err := parseMIME([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(parsed.Text) != "Hello" ||
		len(parsed.Attachments) != 1 ||
		parsed.Attachments[0].Name != "report.txt" ||
		string(parsed.Attachments[0].Content) != "fixture" {
		t.Fatalf("parsed MIME = %#v", parsed)
	}
}

func TestBuildMessageRejectsInjectedReferenceHeaders(t *testing.T) {
	t.Parallel()
	client := &Client{sender: "reader@example.invalid"}
	for _, malicious := range []string{
		"<message@example.invalid>\r\nBcc: attacker@example.invalid",
		"<message@example.invalid>\nX-Injected: true",
		"message@example.invalid",
	} {
		_, _, err := client.buildMessage(mailComposition{
			To:        []string{"recipient@example.invalid"},
			Subject:   "Synthetic reply",
			Body:      "Hello",
			InReplyTo: malicious,
			References: "<parent@example.invalid> " +
				malicious,
		}, false)
		if err == nil {
			t.Fatalf("malicious message ID %q was accepted", malicious)
		}
	}
}

func TestNormalizeReferencesProducesBoundedHeaderSafeChain(t *testing.T) {
	t.Parallel()
	got, err := normalizeReferences(
		"<first@example.invalid> <first@example.invalid>",
		"<second@example.invalid>",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "<first@example.invalid> <second@example.invalid>" {
		t.Fatalf("normalized references = %q", got)
	}
}

func TestInheritedReplyHeadersDropMalformedInputAndAllowForward(t *testing.T) {
	t.Parallel()
	inReplyTo, references := inheritedReplyHeaders(
		application.MailComposeReply,
		"<current@example.invalid>",
		"<first@example.invalid>, bare@example.invalid "+
			"<broken> <second@example.invalid> \u2603",
	)
	if inReplyTo != "<current@example.invalid>" ||
		references != "<first@example.invalid> <second@example.invalid> <current@example.invalid>" {
		t.Fatalf("inherited headers = %q, %q", inReplyTo, references)
	}
	inReplyTo, references = inheritedReplyHeaders(
		application.MailComposeReplyAll,
		"",
		"<first@example.invalid>",
	)
	if inReplyTo != "" || references != "" {
		t.Fatalf("missing Message-ID inherited headers = %q, %q", inReplyTo, references)
	}
	inReplyTo, references = inheritedReplyHeaders(
		application.MailComposeForward,
		"malformed",
		"<first@example.invalid>",
	)
	if inReplyTo != "" || references != "" {
		t.Fatalf("forward inherited headers = %q, %q", inReplyTo, references)
	}
}

func TestIMAPReplyTargetPrefersReplyToWithoutMixingFrom(t *testing.T) {
	t.Parallel()
	replyTo := &imap.Address{
		PersonalName: "Replies",
		MailboxName:  "replies",
		HostName:     "example.invalid",
	}
	from := &imap.Address{
		PersonalName: "Sender",
		MailboxName:  "sender",
		HostName:     "example.invalid",
	}
	envelope := &imap.Envelope{
		ReplyTo: []*imap.Address{replyTo},
		From:    []*imap.Address{from},
	}
	got, err := derivedIMAPAddresses(
		imapReplyTarget(envelope),
		nil,
		"reader@example.invalid",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "replies@example.invalid" {
		t.Fatalf("reply recipients = %#v", got)
	}
	cc := imapAddressesExcluding(
		[]string{"observer@example.invalid", "replies@example.invalid"},
		got,
	)
	if !slices.Equal(cc, []string{"observer@example.invalid"}) {
		t.Fatalf("reply-all Cc recipients = %#v", cc)
	}

	envelope.ReplyTo = nil
	got, err = derivedIMAPAddresses(
		imapReplyTarget(envelope),
		nil,
		"reader@example.invalid",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "sender@example.invalid" {
		t.Fatalf("fallback reply recipients = %#v", got)
	}

	envelope.ReplyTo = []*imap.Address{{MailboxName: "malformed"}}
	if _, err := derivedIMAPAddresses(
		imapReplyTarget(envelope),
		nil,
		"reader@example.invalid",
	); err == nil {
		t.Fatal("malformed Reply-To was accepted")
	}
}

func TestSMTPAuthenticationRejectionUsesSharedClassification(t *testing.T) {
	t.Parallel()

	err := classifyAuthenticationFailure(&smtp.SMTPError{
		Code:         535,
		EnhancedCode: smtp.EnhancedCode{5, 7, 8},
		Message:      "synthetic private provider detail",
	})
	reason, ok := application.ProviderAuthenticationReason(err)
	if !ok || reason != application.AuthenticationReasonCredentialRejected ||
		strings.Contains(err.Error(), "private provider detail") {
		t.Fatalf("classification = %q, error = %v", reason, err)
	}
}
