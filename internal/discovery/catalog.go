package discovery

import (
	"slices"
	"strings"
)

// SignalFamily is declarative, credential-free provider knowledge shared with
// the public compatibility checker artifact. Values are provider families,
// never authorization or capability claims.
type SignalFamily struct {
	ID                        string   `json:"id"`
	DisplayName               string   `json:"displayName"`
	KnownDomains              []string `json:"knownDomains,omitempty"`
	MailExchangeSuffixes      []string `json:"mailExchangeSuffixes,omitempty"`
	AutodiscoverCNAMESuffixes []string `json:"autodiscoverCnameSuffixes,omitempty"`
}

const (
	familyMicrosoftConsumer = "microsoft-consumer"
	familyMicrosoft365      = "microsoft-365"
	familyGoogleConsumer    = "google-consumer"
	familyGoogleWorkspace   = "google-workspace"
	familyAppleICloud       = "apple-icloud"
)

var signalFamilies = []SignalFamily{
	{
		ID: familyMicrosoftConsumer, DisplayName: "Microsoft Outlook.com",
		KnownDomains: []string{"hotmail.com", "live.com", "msn.com", "outlook.com"},
	},
	{
		ID: familyMicrosoft365, DisplayName: "Microsoft 365 / Exchange Online",
		MailExchangeSuffixes:      []string{"mail.protection.outlook.com"},
		AutodiscoverCNAMESuffixes: []string{"autodiscover.outlook.com"},
	},
	{
		ID: familyGoogleConsumer, DisplayName: "Google Gmail",
		KnownDomains: []string{"gmail.com", "googlemail.com"},
	},
	{
		ID: familyGoogleWorkspace, DisplayName: "Google Workspace",
		MailExchangeSuffixes: []string{"aspmx.l.google.com", "google.com"},
	},
	{
		ID: familyAppleICloud, DisplayName: "Apple iCloud",
		KnownDomains: []string{"icloud.com", "mac.com", "me.com"},
	},
}

// SignalCatalog returns a detached, deterministic snapshot for generated
// public artifacts and tests.
func SignalCatalog() []SignalFamily {
	result := make([]SignalFamily, len(signalFamilies))
	for index, family := range signalFamilies {
		result[index] = family
		result[index].KnownDomains = slices.Clone(family.KnownDomains)
		result[index].MailExchangeSuffixes = slices.Clone(family.MailExchangeSuffixes)
		result[index].AutodiscoverCNAMESuffixes = slices.Clone(
			family.AutodiscoverCNAMESuffixes,
		)
	}
	return result
}

func knownDomainFamily(domainName string) string {
	for _, family := range signalFamilies {
		if slices.Contains(family.KnownDomains, domainName) {
			return family.ID
		}
	}
	return ""
}

func mailExchangeFamily(host string) string {
	for _, family := range signalFamilies {
		for _, suffix := range family.MailExchangeSuffixes {
			if host == suffix || strings.HasSuffix(host, "."+suffix) {
				return family.ID
			}
		}
	}
	return ""
}
