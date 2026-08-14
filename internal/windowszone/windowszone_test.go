package windowszone

import "testing"

func TestIANAUsesTerritoryNeutralCLDRMappings(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"GMT Standard Time":     "Europe/London",
		"Pacific Standard Time": "America/Los_Angeles",
		"Tokyo Standard Time":   "Asia/Tokyo",
	}
	for windowsID, want := range tests {
		if got, ok := IANA(windowsID); !ok || got != want {
			t.Errorf("IANA(%q) = %q, %t; want %q, true", windowsID, got, ok, want)
		}
	}
	if _, ok := IANA("Unknown Standard Time"); ok {
		t.Fatal("IANA() accepted an unknown Windows identifier")
	}
}
