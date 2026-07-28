package imapmail

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
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
	mu         sync.Mutex
	messages   [][]byte
	recipients [][]string
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
	return []string{sasl.Plain}
}

func (session *syntheticSMTPSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, username, password string) error {
		if username != "username" || password != "password" {
			return errors.New("invalid synthetic credentials")
		}
		session.authorized = true
		return nil
	}), nil
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
	session.backend.mu.Unlock()
	return nil
}

func (*syntheticSMTPSession) Reset()        {}
func (*syntheticSMTPSession) Logout() error { return nil }

type standardsFixture struct {
	imap Endpoint
	smtp Endpoint
	tls  *tls.Config
	out  *syntheticSMTPBackend
}

func newStandardsFixture(t *testing.T) standardsFixture {
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sent.ID, "smtp1_") {
		t.Fatalf("send result = %#v", sent)
	}
	fixture.out.mu.Lock()
	defer fixture.out.mu.Unlock()
	if len(fixture.out.messages) != 1 ||
		!bytes.Contains(fixture.out.messages[0], []byte("Synthetic submission")) ||
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
