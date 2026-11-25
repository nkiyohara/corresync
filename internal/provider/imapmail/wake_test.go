package imapmail

import (
	"net"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
)

// unreachableEndpoint returns a loopback endpoint on which nothing listens,
// which is how a host looks right after wake before the network returns.
func unreachableEndpoint(t *testing.T, mode string) Endpoint {
	t.Helper()
	var config net.ListenConfig
	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port := splitListener(t, listener)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return Endpoint{Host: host, Port: port, Mode: mode}
}

// IMAP and SMTP open a fresh authenticated connection for every operation,
// so no connection survives sleep in a half-open state and the same client
// recovers as soon as the server is reachable again.
func TestStandardsMailNetworkLossIsNotSignOutAndTheSameClientRecovers(t *testing.T) {
	t.Parallel()
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
	inbox := application.MailListInput{
		Folder: application.MailFolder{
			Kind: application.MailFolderDistinguished, ID: "inbox",
		},
		Limit: 25,
	}

	client.imap = unreachableEndpoint(t, fixture.imap.Mode)
	client.smtp = unreachableEndpoint(t, fixture.smtp.Mode)
	_, err = client.ListMessages(t.Context(), inbox)
	if err == nil {
		t.Fatal("offline IMAP read succeeded")
	}
	if _, rejected := application.ProviderAuthenticationReason(err); rejected {
		t.Fatalf("IMAP network loss was classified as authentication: %v", err)
	}
	_, err = client.SendMail(t.Context(), application.MailSendInput{
		Account: "acc_00000000000000000000000000000001",
		To:      []string{"recipient@example.invalid"},
		Subject: "Offline send", Body: "Synthetic body",
	})
	if err == nil {
		t.Fatal("offline SMTP submission succeeded")
	}
	if _, rejected := application.ProviderAuthenticationReason(err); rejected {
		t.Fatalf("SMTP network loss was classified as authentication: %v", err)
	}
	fixture.out.mu.Lock()
	submitted := len(fixture.out.messages)
	fixture.out.mu.Unlock()
	if submitted != 0 {
		t.Fatalf("offline submission reached the server %d times", submitted)
	}

	client.imap = fixture.imap
	client.smtp = fixture.smtp
	page, err := client.ListMessages(t.Context(), inbox)
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("read after reconnect = %+v, %v", page, err)
	}
}
