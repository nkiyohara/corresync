package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/browser"
	"github.com/nkiyohara/corresync/internal/buildinfo"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/daemonapi"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/localipc"
)

type failingOnboardingDiscoverer struct {
	err error
}

func (discoverer failingOnboardingDiscoverer) Discover(
	context.Context,
	string,
) (application.AccountDiscoveryObservation, error) {
	return application.AccountDiscoveryObservation{}, discoverer.err
}

type sequenceOnboardingDiscoverer struct {
	observations []application.AccountDiscoveryObservation
	err          error
	calls        int
}

func (discoverer *sequenceOnboardingDiscoverer) Discover(
	context.Context,
	string,
) (application.AccountDiscoveryObservation, error) {
	if discoverer.calls >= len(discoverer.observations) {
		return application.AccountDiscoveryObservation{}, discoverer.err
	}
	observation := discoverer.observations[discoverer.calls]
	discoverer.calls++
	return observation, nil
}

func TestGuidedSetupAddsReviewedOutlookAccountWithoutAuthentication(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	discoverer := &accountDiscovererStub{observation: application.AccountDiscoveryObservation{
		Candidates: []application.ProviderCandidate{{
			Provider: domain.ProviderMicrosoftOWA, Confidence: 98,
			Authentication: application.DiscoveryBrowserFirstParty,
			Endpoints: []application.DiscoveredEndpoint{{
				Kind: "origin", Value: "https://outlook.example.invalid",
			}},
			Evidence: []application.DiscoveryEvidence{{
				Source: "test", Detail: "synthetic Outlook route",
			}},
		}},
	}}
	app, stdout := guidedSetupRuntime(t, path, discoverer,
		"reader@example.invalid\npersonal\ny\n4\n3\n")

	if err := (&setupCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account, exists := configured.Accounts["personal"]
	if !exists || configured.DefaultAccount != "personal" ||
		account.Mail == nil || account.Calendar == nil ||
		account.Mail.Provider != domain.ProviderMicrosoftOWA ||
		account.Calendar.Provider != domain.ProviderMicrosoftOWA {
		t.Fatalf("guided account = %+v", configured)
	}
	for _, expected := range []string{
		"Guided account setup",
		"Review account before adding",
		"https://outlook.example.invalid",
		"authentication and monitoring are still off",
		"Continue to agent setup",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("guided setup output missing %q: %q", expected, stdout.String())
		}
	}
}

func TestGuidedSetupConfiguresStandardsRoutesByExternalHandles(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	discoverer := &accountDiscovererStub{observation: application.AccountDiscoveryObservation{
		Candidates: []application.ProviderCandidate{
			{
				Provider: domain.ProviderIMAPSMTP, Confidence: 82,
				Authentication:            application.DiscoveryExternalCredential,
				RequiresExplicitSelection: true,
				Endpoints: []application.DiscoveredEndpoint{
					{Kind: "imap", Value: "imap.example.invalid:993"},
					{Kind: "smtp", Value: "smtp.example.invalid:587"},
				},
				Evidence: []application.DiscoveryEvidence{{
					Source: "test", Detail: "synthetic IMAP route",
				}},
			},
			{
				Provider: domain.ProviderCalDAV, Confidence: 80,
				Authentication:            application.DiscoveryExternalCredential,
				RequiresExplicitSelection: true,
				Endpoints: []application.DiscoveredEndpoint{{
					Kind: "caldav", Value: "https://calendar.example.invalid/dav/",
				}},
				Evidence: []application.DiscoveryEvidence{{
					Source: "test", Detail: "synthetic CalDAV route",
				}},
			},
		},
	}}
	app, _ := guidedSetupRuntime(
		t,
		path,
		discoverer,
		strings.Join([]string{
			"reader@example.invalid",
			"1",
			"standards",
			"1",
			"standards-mail",
			"y",
			"1",
			"standards-calendar",
			"y",
			"y",
			"4",
			"3",
		}, "\n")+"\n",
	)

	if err := (&setupCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configured.Accounts["standards"]
	if account.Mail == nil || account.Mail.IMAPSMTP == nil ||
		account.Calendar == nil || account.Calendar.CalDAV == nil ||
		account.Mail.IMAPSMTP.Credential.Key != "standards-mail" ||
		account.Calendar.CalDAV.Credential.Key != "standards-calendar" {
		t.Fatalf("standards account = %+v", account)
	}
}

func TestGuidedSetupAddsICloudMailAndCalendarWithOneExternalCredential(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	discoverer := &accountDiscovererStub{observation: application.AccountDiscoveryObservation{
		Candidates: []application.ProviderCandidate{
			{
				Provider: domain.ProviderIMAPSMTP, Confidence: 98,
				Authentication: application.DiscoveryExternalCredential,
				Endpoints: []application.DiscoveredEndpoint{
					{Kind: "imap", Value: "imap.mail.me.com:993"},
					{Kind: "smtp", Value: "smtp.mail.me.com:587"},
				},
				Evidence: []application.DiscoveryEvidence{{
					Source: "known_domain", Detail: "icloud.com",
				}},
			},
			{
				Provider: domain.ProviderCalDAV, Confidence: 98,
				Authentication: application.DiscoveryExternalCredential,
				Endpoints: []application.DiscoveredEndpoint{{
					Kind: "caldav", Value: "https://caldav.icloud.com:443/",
				}},
				Evidence: []application.DiscoveryEvidence{{
					Source: "known_domain", Detail: "icloud.com",
				}},
			},
		},
	}}
	app, stdout := guidedSetupRuntime(
		t,
		path,
		discoverer,
		strings.Join([]string{
			"reader@icloud.com",
			"1",
			"icloud-personal",
			"reader@icloud.com",
			"1",
			"icloud-personal-shared",
			"y",
			"y",
			"2",
			"6",
			"3",
		}, "\n")+"\n",
	)
	var commandName string
	var commandArguments []string
	app.runCommand = func(
		_ context.Context,
		_, _ io.Writer,
		name string,
		arguments ...string,
	) error {
		commandName = name
		commandArguments = append([]string(nil), arguments...)
		return nil
	}

	if err := (&setupCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	account := configured.Accounts["icloud-personal"]
	if account.Mail == nil || account.Mail.IMAPSMTP == nil ||
		account.Calendar == nil || account.Calendar.CalDAV == nil {
		t.Fatalf("iCloud account = %+v", account)
	}
	mail := account.Mail.IMAPSMTP
	calendar := account.Calendar.CalDAV
	if mail.IMAP.Host != "imap.mail.me.com" || mail.IMAP.Port != 993 ||
		mail.IMAP.Mode != config.TLSImplicit ||
		mail.SMTP.Host != "smtp.mail.me.com" || mail.SMTP.Port != 587 ||
		mail.SMTP.Mode != config.TLSStartTLS ||
		mail.Username != "reader@icloud.com" ||
		calendar.Endpoint != "https://caldav.icloud.com:443/" ||
		calendar.Username != "reader@icloud.com" {
		t.Fatalf("iCloud routes = mail %+v, calendar %+v", mail, calendar)
	}
	if mail.Credential != calendar.Credential ||
		mail.Credential.Backend != config.CredentialOSKeyring ||
		mail.Credential.Key != "icloud-personal-shared" ||
		!mail.Credential.Consent {
		t.Fatalf("iCloud credential references = %+v, %+v", mail.Credential, calendar.Credential)
	}
	wantCommand, wantArguments, err := iCloudCredentialEnrollmentCommand(
		app.info.OS,
		"icloud-personal-shared",
	)
	if err != nil {
		t.Fatal(err)
	}
	if commandName != wantCommand || !slices.Equal(commandArguments, wantArguments) {
		t.Fatalf("OS credential command = %q %#v", commandName, commandArguments)
	}
	for _, expected := range []string{
		"iCloud Mail + Calendar",
		"two-factor authentication",
		"Mail identity",
		"Calendar identity",
		"OS-owned store completed the prompt",
	} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("iCloud onboarding output missing %q: %q", expected, stdout.String())
		}
	}
}

func TestICloudPresetRequiresKnownAddressOrCompleteSRVEvidence(t *testing.T) {
	t.Parallel()
	mail := &application.ProviderCandidate{
		Provider: domain.ProviderIMAPSMTP,
		Endpoints: []application.DiscoveredEndpoint{
			{Kind: "imap", Value: "imap.mail.me.com:993"},
			{Kind: "smtp", Value: "smtp.mail.me.com:587"},
		},
		Evidence: []application.DiscoveryEvidence{{Source: "manual", Detail: "test"}},
	}
	calendar := &application.ProviderCandidate{
		Provider: domain.ProviderCalDAV,
		Endpoints: []application.DiscoveredEndpoint{{
			Kind: "caldav", Value: "https://caldav.icloud.com:443/",
		}},
		Evidence: []application.DiscoveryEvidence{{Source: "manual", Detail: "test"}},
	}
	plan := newOnboardingRoutePlan(mail, calendar)
	if isICloudOnboardingPlan(application.AccountDiscoveryResult{
		Domain: "example.invalid",
	}, plan) {
		t.Fatal("arbitrary custom domain was classified from unverified evidence")
	}
	mail.Evidence = []application.DiscoveryEvidence{
		{Source: "srv_imaps", Detail: "example.invalid"},
		{Source: "srv_submission", Detail: "example.invalid"},
	}
	calendar.Evidence = []application.DiscoveryEvidence{{
		Source: "srv_caldavs", Detail: "example.invalid",
	}}
	if !isICloudOnboardingPlan(application.AccountDiscoveryResult{
		Domain: "example.invalid",
	}, plan) {
		t.Fatal("complete Apple SRV evidence was not recognized")
	}
}

func TestICloudCredentialEnrollmentUsesOnlyOSOwnedPrompts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		goos string
		name string
		args string
	}{
		{
			goos: "darwin", name: "/usr/bin/security",
			args: "add-generic-password -U -s corresync -a private-handle -l Corresync iCloud -w",
		},
		{
			goos: "linux", name: "secret-tool",
			args: "store --label=Corresync iCloud service corresync username private-handle",
		},
		{
			goos: "windows", name: "cmdkey.exe",
			args: "/generic:corresync:private-handle /user:private-handle",
		},
	} {
		t.Run(test.goos, func(t *testing.T) {
			t.Parallel()
			name, arguments, err := iCloudCredentialEnrollmentCommand(
				test.goos,
				"private-handle",
			)
			if err != nil {
				t.Fatal(err)
			}
			if name != test.name || strings.Join(arguments, " ") != test.args {
				t.Fatalf("command = %q %q", name, strings.Join(arguments, " "))
			}
			if strings.Contains(strings.Join(arguments, " "), "example-app-password") {
				t.Fatal("credential value entered the process arguments")
			}
		})
	}
}

func TestGuidedSetupCancellationLeavesNoPartialAccount(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	discoverer := &accountDiscovererStub{observation: application.AccountDiscoveryObservation{
		Candidates: []application.ProviderCandidate{{
			Provider: domain.ProviderMicrosoftOWA, Confidence: 98,
			Authentication: application.DiscoveryBrowserFirstParty,
			Endpoints: []application.DiscoveredEndpoint{{
				Kind: "origin", Value: "https://outlook.example.invalid",
			}},
			Evidence: []application.DiscoveryEvidence{{
				Source: "test", Detail: "synthetic Outlook route",
			}},
		}},
	}}
	app, stdout := guidedSetupRuntime(
		t, path, discoverer, "reader@example.invalid\npersonal\nn\n",
	)

	if err := (&setupCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(configured.Accounts) != 0 || configured.DefaultAccount != "" {
		t.Fatalf("cancelled setup persisted an account: %+v", configured)
	}
	if !strings.Contains(stdout.String(), "new empty, secret-free configuration remains") {
		t.Fatalf("cancellation output = %q", stdout.String())
	}
}

func TestGuidedSetupAccessibleCancellationLeavesNoPartialAccount(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	app, stdout := guidedSetupRuntime(
		t,
		path,
		failingOnboardingDiscoverer{err: errors.New("discovery must not run")},
		":cancel\n",
	)

	if err := (&setupCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(path)
	if err != nil || len(configured.Accounts) != 0 {
		t.Fatalf("accessible cancellation config = %+v, %v", configured, err)
	}
	if !strings.Contains(stdout.String(), "type :cancel to cancel") ||
		!strings.Contains(stdout.String(), "Guided setup stopped") {
		t.Fatalf("accessible cancellation output = %q", stdout.String())
	}
}

func TestGuidedSetupAccessibleEOFNeverStartsPostAddAuthentication(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	discoverer := &accountDiscovererStub{observation: application.AccountDiscoveryObservation{
		Candidates: []application.ProviderCandidate{{
			Provider:       domain.ProviderMicrosoftOWA,
			Confidence:     98,
			Authentication: application.DiscoveryBrowserFirstParty,
			Endpoints: []application.DiscoveredEndpoint{{
				Kind: "origin", Value: "https://outlook.example.invalid",
			}},
			Evidence: []application.DiscoveryEvidence{{
				Source: "test", Detail: "synthetic Outlook route",
			}},
		}},
	}}
	app, _ := guidedSetupRuntime(
		t,
		path,
		discoverer,
		"reader@example.invalid\npersonal\ny\n",
	)
	started := false
	app.startDaemon = func(context.Context, string) error {
		started = true
		return errors.New("authentication must not start at EOF")
	}

	if err := (&setupCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	if started {
		t.Fatal("accessible EOF started the session owner")
	}
	configured, err := config.Load(path)
	if err != nil || len(configured.Accounts) != 1 {
		t.Fatalf("account before safe EOF = %+v, %v", configured, err)
	}
}

func TestGuidedSetupReportsApprovalPendingGoogleWithoutPersistence(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	discoverer := &accountDiscovererStub{observation: application.AccountDiscoveryObservation{
		Candidates: []application.ProviderCandidate{{
			Provider:                  domain.ProviderGoogle,
			Confidence:                98,
			Authentication:            application.DiscoveryExplicitOAuth,
			RequiresExplicitSelection: true,
			Evidence: []application.DiscoveryEvidence{{
				Source: "test", Detail: "synthetic Google route",
			}},
		}},
	}}
	app, stdout := guidedSetupRuntime(t, path, discoverer, "reader@gmail.com\n")

	err := (&setupCommand{}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "awaiting approval") {
		t.Fatalf("Google guided setup error = %v", err)
	}
	configured, loadErr := config.Load(path)
	if loadErr != nil || len(configured.Accounts) != 0 {
		t.Fatalf("Google guided setup persisted an account: %+v, %v", configured, loadErr)
	}
	if !strings.Contains(stdout.String(), "coming soon") {
		t.Fatalf("Google guided setup output = %q", stdout.String())
	}
}

func TestGuidedSetupDiscoveryFailureLeavesOnlyTheReportedEmptyConfig(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	discoveryErr := errors.New("synthetic discovery failure")
	app, stdout := guidedSetupRuntime(
		t,
		path,
		failingOnboardingDiscoverer{err: discoveryErr},
		"reader@example.invalid\n",
	)

	if err := (&setupCommand{}).Run(app); !errors.Is(err, discoveryErr) {
		t.Fatalf("guided discovery error = %v", err)
	}
	configured, err := config.Load(path)
	if err != nil || len(configured.Accounts) != 0 {
		t.Fatalf("failed discovery config = %+v, %v", configured, err)
	}
	if !strings.Contains(stdout.String(), "Provider-neutral configuration created") {
		t.Fatalf("failed discovery did not report the durable change: %q", stdout.String())
	}
}

func TestGuidedSetupReportsEarlierAccountsAfterLaterDiscoveryFailure(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path := filepath.Join(t.TempDir(), "config.toml")
	discoveryErr := errors.New("synthetic second-account failure")
	discoverer := &sequenceOnboardingDiscoverer{
		observations: []application.AccountDiscoveryObservation{{
			Candidates: []application.ProviderCandidate{{
				Provider:       domain.ProviderMicrosoftOWA,
				Confidence:     98,
				Authentication: application.DiscoveryBrowserFirstParty,
				Endpoints: []application.DiscoveredEndpoint{{
					Kind: "origin", Value: "https://outlook.example.invalid",
				}},
				Evidence: []application.DiscoveryEvidence{{
					Source: "test", Detail: "synthetic Outlook route",
				}},
			}},
		}},
		err: discoveryErr,
	}
	app, _ := guidedSetupRuntime(
		t,
		path,
		discoverer,
		"first@example.invalid\nfirst\ny\n4\n1\nsecond@example.invalid\n",
	)

	err := (&setupCommand{}).Run(app)
	if !errors.Is(err, discoveryErr) ||
		!strings.Contains(err.Error(), "after 1 completed account") ||
		!strings.Contains(err.Error(), "remain configured") {
		t.Fatalf("second-account failure = %v", err)
	}
	configured, loadErr := config.Load(path)
	if loadErr != nil || len(configured.Accounts) != 1 {
		t.Fatalf("completed account after later failure = %+v, %v", configured, loadErr)
	}
	if _, exists := configured.Accounts["first"]; !exists {
		t.Fatalf("first account was lost after later failure: %+v", configured)
	}
}

func TestGuidedSetupRestartsAnExistingSessionOwnerAfterAccountCommit(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state")
	t.Setenv("CORRESYNC_STATE_DIR", statePath)
	path := filepath.Join(root, "config.toml")
	if err := config.Save(path, config.OutlookDefault()); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	initialDigest, err := config.Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := localipc.ResolveInState(path, statePath)
	if err != nil {
		t.Fatal(err)
	}
	previous := startLifecycleTestDaemon(
		t.Context(), t, endpoint, daemonapi.ProtocolVersion, buildinfo.Current().Version,
		123, initialDigest, configured.Accounts[configured.DefaultAccount].ID,
	)
	t.Cleanup(previous.stop)

	discoverer := &accountDiscovererStub{observation: application.AccountDiscoveryObservation{
		Candidates: []application.ProviderCandidate{{
			Provider:       domain.ProviderMicrosoftOWA,
			Confidence:     98,
			Authentication: application.DiscoveryBrowserFirstParty,
			Endpoints: []application.DiscoveredEndpoint{{
				Kind: "origin", Value: "https://outlook.example.invalid",
			}},
			Evidence: []application.DiscoveryEvidence{{
				Source: "test", Detail: "synthetic Outlook route",
			}},
		}},
	}}
	app, _ := guidedSetupRuntime(
		t,
		path,
		discoverer,
		"personal@example.invalid\npersonal\n1\ny\n4\n3\n",
	)
	app.endpoint = func(string) (localipc.Endpoint, error) { return endpoint, nil }
	var replacement lifecycleTestDaemon
	app.startDaemon = func(ctx context.Context, configPath string) error {
		if configPath != path {
			return fmt.Errorf("daemon config path = %q, want %q", configPath, path)
		}
		digest, fingerprintErr := config.Fingerprint(path)
		if fingerprintErr != nil {
			return fingerprintErr
		}
		replacement = startLifecycleTestDaemon(
			ctx, t, endpoint, daemonapi.ProtocolVersion, buildinfo.Current().Version,
			456, digest, configured.Accounts[configured.DefaultAccount].ID,
		)
		return nil
	}

	if err := (&setupCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	if replacement.stop == nil {
		t.Fatal("guided setup did not restart the session owner")
	}
	t.Cleanup(replacement.stop)
	if previous.shutdowns.Load() != 1 {
		t.Fatalf("previous session owner shutdowns = %d", previous.shutdowns.Load())
	}
	configured, err = config.Load(path)
	if err != nil || len(configured.Accounts) != 2 {
		t.Fatalf("guided setup accounts = %+v, %v", configured.Accounts, err)
	}
}

func TestOnboardingEndpointValidationMatchesPersistedRouteBoundaries(t *testing.T) {
	for _, invalid := range []string{
		"http://mail.example.invalid",
		"https://user@mail.example.invalid",
		"https://mail.example.invalid/path?token=value",
		"https://mail.example.invalid/path#fragment",
	} {
		if err := validateOnboardingHTTPSURL(invalid); err == nil {
			t.Fatalf("validateOnboardingHTTPSURL(%q) accepted an invalid URL", invalid)
		}
	}
	for _, invalid := range []string{
		"http://127.0.0.1/callback",
		"http://localhost:8080/callback",
		"http://127.0.0.1:8080/callback?token=value",
		"https://127.0.0.1:8080/callback",
	} {
		if err := validateOnboardingLoopbackRedirect(invalid); err == nil {
			t.Fatalf("validateOnboardingLoopbackRedirect(%q) accepted an invalid URL", invalid)
		}
	}
	if err := validateOnboardingHTTPSURL("https://mail.example.invalid/path"); err != nil {
		t.Fatalf("valid HTTPS route rejected: %v", err)
	}
	if err := validateOnboardingLoopbackRedirect("http://127.0.0.1:0/callback"); err != nil {
		t.Fatalf("valid loopback redirect rejected: %v", err)
	}
	for _, invalid := range []string{
		"127.0.0.1", "-mail.example.invalid", "mail..example.invalid", "mail_1.example.invalid",
	} {
		if err := validateOnboardingHostname(invalid); err == nil {
			t.Fatalf("validateOnboardingHostname(%q) accepted an invalid host", invalid)
		}
	}
	if err := validateOnboardingHostname("mail-1.example.invalid"); err != nil {
		t.Fatalf("valid DNS hostname rejected: %v", err)
	}
}

func TestGuidedSetupNeverPromptsInJSONOrNonTTYContexts(t *testing.T) {
	for _, test := range []struct {
		name    string
		command setupCommand
		tty     bool
	}{
		{name: "JSON", command: setupCommand{JSON: true}, tty: true},
		{name: "pipe", command: setupCommand{}, tty: false},
		{name: "flags without address", command: setupCommand{Alias: "work"}, tty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			app := newRuntime(
				t.Context(), path, &bytes.Buffer{}, &bytes.Buffer{}, buildinfo.Current(),
			)
			app.stdin = strings.NewReader("reader@example.invalid\n")
			app.interactiveInput = func() bool { return test.tty }
			app.interactiveStdout = func() bool { return test.tty }
			if err := test.command.Run(app); err == nil {
				t.Fatal("guided setup unexpectedly prompted")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("non-interactive setup created config: %v", err)
			}
		})
	}
}

func TestSettingsAccountAddUsesTheSharedRegistrationWizard(t *testing.T) {
	path := saveSettingsFixture(t)
	discoverer := &accountDiscovererStub{observation: application.AccountDiscoveryObservation{
		Candidates: []application.ProviderCandidate{{
			Provider: domain.ProviderMicrosoftOWA, Confidence: 98,
			Authentication: application.DiscoveryBrowserFirstParty,
			Endpoints: []application.DiscoveredEndpoint{{
				Kind: "origin", Value: "https://outlook.example.invalid",
			}},
			Evidence: []application.DiscoveryEvidence{{
				Source: "test", Detail: "synthetic Outlook route",
			}},
		}},
	}}
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader(
		"1\n1\npersonal@example.invalid\npersonal\n1\ny\n4\n4\n7\n",
	)
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment
	app.accountDiscoverer = discoverer
	app.launch = func(context.Context, browser.Options) (browserHandle, error) {
		t.Fatal("shared account wizard unexpectedly started authentication")
		return nil, nil
	}

	if err := (&settingsCommand{}).Run(app); err != nil {
		t.Fatal(err)
	}
	configured, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configured.Accounts["personal"]; !exists ||
		configured.DefaultAccount != "work" {
		t.Fatalf("settings wizard account = %+v", configured)
	}
	if !strings.Contains(stdout.String(), "Review account before adding") {
		t.Fatalf("settings wizard output = %q", stdout.String())
	}
}

func guidedSetupRuntime(
	t *testing.T,
	path string,
	discoverer application.AccountDiscoverer,
	input string,
) (*runtime, *bytes.Buffer) {
	t.Helper()
	var stdout bytes.Buffer
	app := newRuntime(t.Context(), path, &stdout, &bytes.Buffer{}, buildinfo.Current())
	app.stdin = strings.NewReader(input)
	app.interactiveInput = func() bool { return true }
	app.interactiveStdout = func() bool { return true }
	app.lookupEnv = settingsTestEnvironment
	app.accountDiscoverer = discoverer
	app.launch = func(context.Context, browser.Options) (browserHandle, error) {
		t.Fatal("guided setup unexpectedly started authentication")
		return nil, nil
	}
	return app, &stdout
}
