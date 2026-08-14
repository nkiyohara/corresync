// Package microsoftcloud defines the closed, security-sensitive endpoint
// pairs for Microsoft Graph deployments. A configured API origin and its
// authorization authority are selected together; callers cannot mix clouds.
package microsoftcloud

import (
	"errors"
	"fmt"
)

// ID names one supported Microsoft cloud deployment.
type ID string

const (
	Global  ID = "global"
	GCCHigh ID = "gcc-high"
	DoD     ID = "dod"
	China   ID = "china"
)

// Profile is the immutable public endpoint policy for one deployment.
type Profile struct {
	ID               ID
	APIBase          string
	AuthorizationURL string
	TokenURL         string
	TasksAvailable   bool
}

// #nosec G101 -- This profile contains public OAuth endpoint URLs, not credentials.
var globalProfile = Profile{
	ID: Global, APIBase: "https://graph.microsoft.com/v1.0",
	AuthorizationURL: "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
	TokenURL:         "https://login.microsoftonline.com/common/oauth2/v2.0/token",
	TasksAvailable:   true,
}

// #nosec G101 -- This profile contains public OAuth endpoint URLs, not credentials.
var gccHighProfile = Profile{
	ID: GCCHigh, APIBase: "https://graph.microsoft.us/v1.0",
	AuthorizationURL: "https://login.microsoftonline.us/organizations/oauth2/v2.0/authorize",
	TokenURL:         "https://login.microsoftonline.us/organizations/oauth2/v2.0/token",
	TasksAvailable:   true,
}

// #nosec G101 -- This profile contains public OAuth endpoint URLs, not credentials.
var dodProfile = Profile{
	ID: DoD, APIBase: "https://dod-graph.microsoft.us/v1.0",
	AuthorizationURL: "https://login.microsoftonline.us/organizations/oauth2/v2.0/authorize",
	TokenURL:         "https://login.microsoftonline.us/organizations/oauth2/v2.0/token",
	TasksAvailable:   true,
}

// #nosec G101 -- This profile contains public OAuth endpoint URLs, not credentials.
var chinaProfile = Profile{
	ID: China, APIBase: "https://microsoftgraph.chinacloudapi.cn/v1.0",
	AuthorizationURL: "https://login.chinacloudapi.cn/organizations/oauth2/v2.0/authorize",
	TokenURL:         "https://login.chinacloudapi.cn/organizations/oauth2/v2.0/token",
	TasksAvailable:   false,
}

var profiles = map[ID]Profile{
	Global:  globalProfile,
	GCCHigh: gccHighProfile,
	DoD:     dodProfile,
	China:   chinaProfile,
}

// Resolve returns one exact endpoint profile. An omitted ID preserves the
// pre-national-cloud configuration contract and means the global deployment.
func Resolve(id ID) (Profile, error) {
	if id == "" {
		id = Global
	}
	profile, ok := profiles[id]
	if !ok {
		return Profile{}, fmt.Errorf("unknown Microsoft cloud %q", id)
	}
	return profile, nil
}

// Equivalent reports whether two configuration values select the same closed
// deployment. The empty legacy value is therefore equivalent to Global.
func Equivalent(left, right ID) bool {
	leftProfile, leftErr := Resolve(left)
	rightProfile, rightErr := Resolve(right)
	return leftErr == nil && rightErr == nil && leftProfile.ID == rightProfile.ID
}

// ValidateAPIBase rejects a valid Microsoft origin paired with the wrong
// cloud just as firmly as an arbitrary origin.
func ValidateAPIBase(id ID, apiBase string) error {
	profile, err := Resolve(id)
	if err != nil {
		return err
	}
	if apiBase != profile.APIBase {
		return errors.New("the Microsoft Graph API base does not match the selected cloud")
	}
	return nil
}
