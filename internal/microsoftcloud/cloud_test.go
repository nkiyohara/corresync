package microsoftcloud

import "testing"

func TestProfilesKeepAuthorityAndAPIOriginsPaired(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id    ID
		api   string
		auth  string
		token string
		tasks bool
	}{
		{
			Global, "https://graph.microsoft.com/v1.0",
			"https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			"https://login.microsoftonline.com/common/oauth2/v2.0/token", true,
		},
		{
			GCCHigh, "https://graph.microsoft.us/v1.0",
			"https://login.microsoftonline.us/organizations/oauth2/v2.0/authorize",
			"https://login.microsoftonline.us/organizations/oauth2/v2.0/token", true,
		},
		{
			DoD, "https://dod-graph.microsoft.us/v1.0",
			"https://login.microsoftonline.us/organizations/oauth2/v2.0/authorize",
			"https://login.microsoftonline.us/organizations/oauth2/v2.0/token", true,
		},
		{
			China, "https://microsoftgraph.chinacloudapi.cn/v1.0",
			"https://login.chinacloudapi.cn/organizations/oauth2/v2.0/authorize",
			"https://login.chinacloudapi.cn/organizations/oauth2/v2.0/token", false,
		},
	}
	for _, test := range tests {
		profile, err := Resolve(test.id)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", test.id, err)
		}
		if profile.APIBase != test.api || profile.AuthorizationURL != test.auth ||
			profile.TokenURL != test.token || profile.TasksAvailable != test.tasks {
			t.Errorf("Resolve(%q) = %+v", test.id, profile)
		}
	}
	if profile, err := Resolve(""); err != nil || profile.ID != Global {
		t.Fatalf("Resolve(empty) = %+v, %v", profile, err)
	}
}

func TestValidateAPIBaseRejectsCrossCloudAndUnknownValues(t *testing.T) {
	t.Parallel()
	if err := ValidateAPIBase(GCCHigh, "https://graph.microsoft.us/v1.0"); err != nil {
		t.Fatalf("ValidateAPIBase(correct): %v", err)
	}
	for _, test := range []struct {
		id  ID
		api string
	}{
		{GCCHigh, "https://graph.microsoft.com/v1.0"},
		{ID("future"), "https://graph.microsoft.com/v1.0"},
		{Global, "https://example.invalid/v1.0"},
	} {
		if err := ValidateAPIBase(test.id, test.api); err == nil {
			t.Errorf("ValidateAPIBase(%q, %q) succeeded", test.id, test.api)
		}
	}
}

func TestEquivalentCanonicalizesTheLegacyGlobalValue(t *testing.T) {
	t.Parallel()
	if !Equivalent("", Global) || !Equivalent(Global, Global) {
		t.Fatal("global cloud aliases were not equivalent")
	}
	if Equivalent(Global, GCCHigh) || Equivalent(ID("future"), ID("future")) {
		t.Fatal("distinct or invalid clouds were equivalent")
	}
}
