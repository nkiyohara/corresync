package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/microsoftcloud"
)

func TestConfigLegacyOutlookKeysPreserveSingleServiceRoutes(t *testing.T) {
	for _, service := range []string{"mail", "calendar"} {
		t.Run(service, func(t *testing.T) {
			configuration := config.OutlookDefault()
			account := configuration.Accounts["work"]
			if service == "mail" {
				account.Calendar = nil
			} else {
				account.Mail = nil
			}
			configuration.Accounts["work"] = account
			if err := configuration.Validate(); err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{"origin": "https://outlook.cloud.microsoft", "mailbox": "shared@example.test"} {
				if err := setConfigValue(&configuration, "accounts.work."+key, value); err != nil {
					t.Fatal(err)
				}
				got, err := getConfigValue(configuration, "accounts.work."+key)
				if err != nil || got != value {
					t.Fatalf("%s = %v, %v", key, got, err)
				}
			}
			updated := configuration.Accounts["work"]
			if service == "mail" && updated.Calendar != nil || service == "calendar" && updated.Mail != nil {
				t.Fatal("configuration edit added an unselected service")
			}
		})
	}
}

func TestConfigLegacyOutlookKeysRejectOtherProvidersWithoutChanges(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, key := range []string{"origin", "mailbox"} {
			t.Run(fmt.Sprintf("mixed=%t/%s", mixed, key), func(t *testing.T) {
				configuration := config.OutlookDefault()
				account := configuration.Accounts["work"]
				if !mixed {
					account.Calendar = nil
				}
				account.Mail = &config.MailRoute{Provider: domain.ProviderJMAP, JMAP: &config.JMAPRoute{
					SessionURL: "https://jmap.example.test/session", Username: "reader@example.test",
					Credential: config.CredentialRef{Backend: config.CredentialOSKeyring, Key: "synthetic", Consent: true},
				}}
				configuration.Accounts["work"] = account
				if err := configuration.Validate(); err != nil {
					t.Fatal(err)
				}
				before, _ := json.Marshal(configuration)
				value := "https://outlook.cloud.microsoft"
				if key == "mailbox" {
					value = "shared@example.test"
				}
				if err := setConfigValue(&configuration, "accounts.work."+key, value); err == nil {
					t.Fatal("non-Outlook route accepted legacy Outlook key")
				}
				after, _ := json.Marshal(configuration)
				if !bytes.Equal(before, after) {
					t.Fatal("rejected update changed an existing route")
				}
			})
		}
	}
}

func TestAccountAddPreservesSovereignCloudInDefaultCalendar(t *testing.T) {
	for _, cloud := range []microsoftcloud.ID{microsoftcloud.GCCHigh, microsoftcloud.DoD, microsoftcloud.China} {
		t.Run(string(cloud), func(t *testing.T) {
			app, path, _ := newAccountCommandRuntime(t, &accountDiscovererStub{})
			command := accountAddCommand{
				Address: "reader@example.test", Alias: "sovereign", Provider: string(domain.ProviderMicrosoftGraph),
				MicrosoftCloud: string(cloud), OAuthClientID: "synthetic-public-client",
				OAuthRedirectURI: "http://127.0.0.1:53683/oauth/callback", AuthorizationKey: "synthetic-grant", ApproveOAuth: true,
			}
			if err := command.Run(app); err != nil {
				t.Fatal(err)
			}
			configuration, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			account := configuration.Accounts["sovereign"]
			if account.Mail == nil || account.Calendar == nil {
				t.Fatal("missing configured Graph services")
			}
			if account.Mail.MicrosoftGraph.MicrosoftCloud != cloud || account.Calendar.MicrosoftGraph.MicrosoftCloud != cloud {
				t.Fatal("Graph services lost selected cloud")
			}
			if account.Mail.MicrosoftGraph.APIBase != account.Calendar.MicrosoftGraph.APIBase {
				t.Fatal("Graph services use different API deployments")
			}
		})
	}
}

func TestMailReviewsDiscloseEveryRecipient(t *testing.T) {
	recipients := func(prefix string) []string {
		result := make([]string, 10)
		for i := range result {
			result[i] = fmt.Sprintf("%s%d%s@example.test", prefix, i, strings.Repeat("a", 50))
		}
		return result
	}
	input := application.MailDraftInput{Account: "test-account", To: recipients("to"), CC: recipients("cc"), BCC: recipients("bcc"), Body: "synthetic body"}
	if err := input.Validate(50); err != nil {
		t.Fatal(err)
	}
	draft := application.MailDraftSnapshot{ID: "draft-id", ChangeKey: "change-key", To: input.To, CC: input.CC, BCC: input.BCC, Body: input.Body, BodyFormat: application.MailBodyText}
	draftReview := draft.Review()
	if err := draftReview.Validate(application.MailDraftSendInput{Account: input.Account, DraftID: draft.ID, DraftChangeKey: draft.ChangeKey}, 50); err != nil {
		t.Fatal(err)
	}
	for _, saved := range []bool{false, true} {
		t.Run(fmt.Sprintf("saved=%t", saved), func(t *testing.T) {
			var output bytes.Buffer
			var err error
			if saved {
				err = writeSendDraftReview(&output, draftReview, false)
			} else {
				err = writeSendReview(&output, input.Review(), false)
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, list := range [][]string{input.To, input.CC, input.BCC} {
				for _, address := range list {
					if !strings.Contains(output.String(), address) {
						t.Fatalf("review omitted recipient %s", address)
					}
				}
			}
		})
	}
}
