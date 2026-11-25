package main

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

type signInVerifierStub struct {
	verified bool
	err      error
	calls    int
	caller   domain.Caller
}

func (stub *signInVerifierStub) SignInVerified(_ context.Context, _ domain.AccountID, caller domain.Caller) (bool, error) {
	stub.calls++
	stub.caller = caller
	return stub.verified, stub.err
}

func signInNoticeApp(t *testing.T) (*runtime, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	return &runtime{context: t.Context(), stdout: &stdout, stderr: &stderr, processID: 7}, &stdout, &stderr
}

func imapAccount() config.Account {
	return config.Account{
		ID: "acc_0000000000000000000000000000000d", Address: "ana@example.com",
		Mail: &config.MailRoute{Provider: domain.ProviderIMAPSMTP, IMAPSMTP: &config.IMAPSMTPRoute{
			IMAP:     config.TLSEndpoint{Host: "imap.example.com", Port: 993, Mode: config.TLSImplicit},
			SMTP:     config.TLSEndpoint{Host: "smtp.example.com", Port: 465, Mode: config.TLSImplicit},
			Username: "ana",
			Credential: config.CredentialRef{
				Backend: config.CredentialOSKeyring, Key: "imap-ana", Consent: true,
			},
		}},
	}
}

func TestSignInNoticeNamesEachDestinationByRouteKind(t *testing.T) {
	t.Parallel()
	google := sessionGoogleOAuthRoute("https://www.googleapis.com/calendar/v3", "google-work")
	outlook := config.OutlookDefault().Accounts["work"]
	outlook.Address = "ana@example.com"
	jmap := config.Account{
		ID: "acc_0000000000000000000000000000000e",
		Mail: &config.MailRoute{Provider: domain.ProviderJMAP, JMAP: &config.JMAPRoute{
			SessionURL: "https://jmap.example.com/.well-known/jmap", Username: "ana",
			Credential: config.CredentialRef{Backend: config.CredentialOSKeyring, Key: "jmap", Consent: true},
		}},
		Calendar: &config.CalendarRoute{Provider: domain.ProviderGoogle, Google: &google},
	}
	tests := []struct {
		name    string
		account config.Account
		want    string
		hosts   []string
	}{
		{
			name: "dedicated browser", account: outlook,
			want: "Signing in to work (ana@example.com)\n" +
				"  Outlook Web (mail, calendar): outlook.cloud.microsoft\n",
			hosts: []string{"outlook.cloud.microsoft"},
		},
		{
			name: "saved password and provider authorization", account: jmap,
			want: "Signing in to work\n" +
				"  JMAP (mail): jmap.example.com:443 (uses the saved password from your keyring)\n" +
				"  Gmail / Google (calendar): accounts.google.com, OAuth client\n" +
				"  synthetic-public-client-google-work (opens in your browser)\n",
			hosts: []string{"jmap.example.com:443", "accounts.google.com"},
		},
		{
			name: "IMAP and SMTP", account: imapAccount(),
			want: "Signing in to work (ana@example.com)\n" +
				"  IMAP / SMTP (mail): imap.example.com:993, smtp.example.com:465 (uses the saved\n" +
				"  password from your keyring)\n",
			hosts: []string{"imap.example.com:993", "smtp.example.com:465"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			routes, routeErr := signInDestinations(test.account)
			if routeErr != nil {
				t.Fatalf("signInDestinations() error = %v", routeErr)
			}
			var output bytes.Buffer
			hosts, err := writeSignInDestination(&output, 80, "work", test.account, routes, nil)
			if err != nil {
				t.Fatal(err)
			}
			if output.String() != test.want || !slices.Equal(hosts, test.hosts) {
				t.Fatalf("notice =\n%q\nhosts %v\nwant\n%q\nhosts %v", output.String(), hosts, test.want, test.hosts)
			}
		})
	}
}

func TestFirstSignInPrintsTheDestinationAndContinues(t *testing.T) {
	app, stdout, stderr := signInNoticeApp(t)
	verifier := &signInVerifierStub{}
	account := config.OutlookDefault().Accounts["work"]
	if err := reviewSignInDestination(app, verifier, "work", account); err != nil {
		t.Fatalf("review error = %v", err)
	}
	if verifier.calls != 1 || verifier.caller.Surface != "cli" {
		t.Fatalf("verification query = %+v", verifier)
	}
	if !strings.Contains(stderr.String(), "outlook.cloud.microsoft") ||
		!strings.Contains(stderr.String(), "This is the first sign-in with these connection settings.") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	// --json output goes to stdout and stays exactly as before.
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want nothing", stdout.String())
	}
}

func TestVerifiedSignInOmitsTheFirstTimeNote(t *testing.T) {
	app, stdout, stderr := signInNoticeApp(t)
	err := reviewSignInDestination(app, &signInVerifierStub{verified: true}, "work",
		config.OutlookDefault().Accounts["work"])
	if err != nil || !strings.Contains(stderr.String(), "outlook.cloud.microsoft") ||
		strings.Contains(stderr.String(), "first sign-in") || stdout.Len() != 0 {
		t.Fatalf("review error = %v stderr = %q stdout = %q", err, stderr.String(), stdout.String())
	}
}

// An older daemon cannot answer the verification query; the notice then
// treats the settings as new rather than failing the sign-in.
func TestUnknownVerificationStillSignsIn(t *testing.T) {
	app, _, stderr := signInNoticeApp(t)
	err := reviewSignInDestination(app, &signInVerifierStub{err: errors.New("old daemon")}, "work",
		config.OutlookDefault().Accounts["work"])
	if err != nil || !strings.Contains(stderr.String(), "This is the first sign-in") {
		t.Fatalf("review error = %v stderr = %q", err, stderr.String())
	}
}

// The MCP rename-and-add scenario ends here: the person runs the command the
// agent relayed and is shown the look-alike host before anything opens.
func TestLookAlikeAccountShowsItsHostBeforeSigningIn(t *testing.T) {
	configuration := cloneSignInConfig(config.OutlookDefault())
	configuration.Accounts["work-old"] = configuration.Accounts["work"]
	lookAlike := configuration.Accounts["work"]
	lookAlike.ID = "acc_0000000000000000000000000000000b"
	configuration.Accounts["work"] = lookAlike
	configuration = withOutlookOrigin(configuration, "https://outlook-office.example")

	app, _, stderr := signInNoticeApp(t)
	if err := reviewSignInDestination(app, &signInVerifierStub{}, "work", configuration.Accounts["work"]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "Outlook Web (mail, calendar): outlook-office.example") ||
		!strings.Contains(stderr.String(), "This is the first sign-in with these connection settings.") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestSignInNoticeRefusesToRenderLookAlikeHosts(t *testing.T) {
	configuration := withOutlookOrigin(cloneSignInConfig(config.OutlookDefault()), "https://outlоok.example")
	app, _, stderr := signInNoticeApp(t)
	if err := reviewSignInDestination(app, &signInVerifierStub{}, "work", configuration.Accounts["work"]); err != nil {
		t.Fatalf("review error = %v", err)
	}
	if strings.Contains(stderr.String(), "о") ||
		!strings.Contains(stderr.String(), "cannot be shown in") ||
		!strings.Contains(stderr.String(), "\n    corr account show work\n") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
