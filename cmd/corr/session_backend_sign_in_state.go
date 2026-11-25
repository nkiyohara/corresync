package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/oauthlocal"
)

const (
	signInStateFormatVersion = 1
	signInStateMaxBytes      = 1 << 20
	signInCooldownFile       = "cooldowns.json"
	signInVerifiedFile       = "cli-verified-routes.json"
)

var (
	errSignInRouteUnsupported       = errors.New("account route cannot be offered for an MCP sign-in")
	errSignInDestinationUndisplayed = errors.New("account sign-in destination cannot be displayed safely")
)

// signInStateFiles persists the daemon-owned sign-in state under
// <state>/sign-in with owner-only permissions. An empty dir means the state
// directory could not be resolved; callers then fail closed.
type signInStateFiles struct {
	dir string
}

// signInCooldownDocument is cooldowns.json: account ID -> end of cool-down.
type signInCooldownDocument struct {
	Version   int                                       `json:"version"`
	Cooldowns map[domain.AccountID]signInCooldownRecord `json:"cooldowns"`
}

type signInCooldownRecord struct {
	Until    time.Time `json:"until"`
	Declined bool      `json:"declined"`
}

// signInVerifiedDocument is cli-verified-routes.json: account ID -> SHA-256
// over the account's security-relevant connection settings at the time a
// sign-in started from the CLI last completed.
type signInVerifiedDocument struct {
	Version  int                         `json:"version"`
	Accounts map[domain.AccountID]string `json:"accounts"`
}

func (files signInStateFiles) path(name string) (string, error) {
	if files.dir == "" {
		return "", errors.New("sign-in state directory is unavailable")
	}
	return filepath.Join(files.dir, name), nil
}

func (files signInStateFiles) read(name string, destination any) (bool, error) {
	path, err := files.path(name)
	if err != nil {
		return false, err
	}
	file, err := os.Open(path) //nolint:gosec // Fixed name under the private state directory.
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, signInStateMaxBytes+1))
	if err != nil {
		return false, err
	}
	if len(data) > signInStateMaxBytes {
		return false, errors.New("sign-in state file is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return false, fmt.Errorf("decode sign-in state: %w", err)
	}
	return true, nil
}

func (files signInStateFiles) write(name string, value any) error {
	path, err := files.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(files.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(files.dir, 0o700); err != nil { //nolint:gosec // Owner-only directory.
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(files.dir, "."+name+".*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (files signInStateFiles) loadCooldowns(now time.Time) (map[domain.AccountID]signInCooldown, error) {
	var document signInCooldownDocument
	exists, err := files.read(signInCooldownFile, &document)
	if err != nil {
		return nil, err
	}
	cooldowns := make(map[domain.AccountID]signInCooldown)
	if !exists {
		return cooldowns, nil
	}
	if document.Version != signInStateFormatVersion {
		return nil, errors.New("unsupported sign-in cool-down state version")
	}
	for account, record := range document.Cooldowns {
		if err := account.ValidateOpaque(); err != nil {
			return nil, err
		}
		if now.Before(record.Until) {
			cooldowns[account] = signInCooldown{until: record.Until, declined: record.Declined}
		}
	}
	return cooldowns, nil
}

func (files signInStateFiles) saveCooldowns(cooldowns map[domain.AccountID]signInCooldown, now time.Time) error {
	document := signInCooldownDocument{
		Version:   signInStateFormatVersion,
		Cooldowns: make(map[domain.AccountID]signInCooldownRecord, len(cooldowns)),
	}
	for account, cooldown := range cooldowns {
		if now.Before(cooldown.until) {
			document.Cooldowns[account] = signInCooldownRecord{Until: cooldown.until.UTC(), Declined: cooldown.declined}
		}
	}
	return files.write(signInCooldownFile, document)
}

func (files signInStateFiles) loadVerified() (map[domain.AccountID]string, error) {
	var document signInVerifiedDocument
	exists, err := files.read(signInVerifiedFile, &document)
	if err != nil {
		return nil, err
	}
	verified := make(map[domain.AccountID]string)
	if !exists {
		return verified, nil
	}
	if document.Version != signInStateFormatVersion {
		return nil, errors.New("unsupported sign-in verification state version")
	}
	for account, digest := range document.Accounts {
		if err := account.ValidateOpaque(); err != nil {
			return nil, err
		}
		if decoded, err := hex.DecodeString(digest); err != nil || len(decoded) != sha256.Size {
			return nil, errors.New("sign-in verification digest is malformed")
		}
		verified[account] = digest
	}
	return verified, nil
}

func (files signInStateFiles) saveVerified(verified map[domain.AccountID]string) error {
	return files.write(signInVerifiedFile, signInVerifiedDocument{
		Version: signInStateFormatVersion, Accounts: verified,
	})
}

// signInRouteDigest commits to every enabled connection setting that decides
// where a sign-in goes or which credential it reads: address, providers,
// route kinds, origins, endpoints, OAuth client IDs, clouds, tenants, and
// credential handles. Any change, through MCP or the CLI, changes it.
func signInRouteDigest(account config.Account) (string, error) {
	active := account.ActiveServices()
	encoded, err := json.Marshal(struct {
		Address  string                 `json:"address"`
		Mail     *config.MailRoute      `json:"mail"`
		Calendar *config.CalendarRoute  `json:"calendar"`
		Tasks    *config.TaskRoute      `json:"tasks"`
		Messages *config.MessagingRoute `json:"messages"`
	}{active.Address, active.Mail, active.Calendar, active.Tasks, active.Messages})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("corresync/sign-in-routes/v1\n"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

func signInHost(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", errSignInDestinationUndisplayed
	}
	host := strings.ToLower(parsed.Host)
	if !application.AccountSignInHostDisplayable(host) {
		return "", errSignInDestinationUndisplayed
	}
	return host, nil
}

func oauthServiceCovered(services oauthlocal.Services, service application.AuthenticationService) bool {
	switch service {
	case application.AuthenticationServiceMail:
		return services.Mail
	case application.AuthenticationServiceCalendar:
		return services.Calendar
	case application.AuthenticationServiceTasks:
		return services.Tasks
	case application.AuthenticationServiceMessages:
		return false
	default:
		return false
	}
}

// signInDestinations describes, from configuration and fixed provider
// endpoints only, where approving each released and enabled service signs in.
func signInDestinations(configured config.Account) ([]application.AccountSignInRoute, error) {
	active, _, err := releasedSessionRoutes(configured)
	if err != nil {
		return nil, errSignInRouteUnsupported
	}
	consents, err := accountOAuthConsents(active)
	if err != nil {
		return nil, errSignInRouteUnsupported
	}
	services := configuredAuthenticationServices(active)
	if len(services) == 0 {
		return nil, errSignInRouteUnsupported
	}
	routes := make([]application.AccountSignInRoute, 0, len(services))
	for _, service := range services {
		route := application.AccountSignInRoute{
			Service: service, Provider: configuredServiceProvider(active, service),
			Kind: application.AccountSignInStoredCredential,
		}
		var hostSource string
		switch {
		case route.Provider == domain.ProviderMicrosoftOWA:
			web, ok := active.OutlookWeb()
			if !ok {
				return nil, errSignInRouteUnsupported
			}
			route.Kind, hostSource = application.AccountSignInDedicatedBrowser, web.Origin
		case service == application.AuthenticationServiceMessages:
			switch messages := active.Messages; {
			case messages != nil && messages.TeamsWeb != nil:
				route.Kind, hostSource = application.AccountSignInDedicatedBrowser, messages.TeamsWeb.Web.Origin
			case messages != nil && messages.Mattermost != nil:
				hostSource = messages.Mattermost.Origin
			case messages != nil && messages.Slack != nil:
				hostSource = messages.Slack.APIBase
			default:
				return nil, errSignInRouteUnsupported
			}
		case route.Provider == domain.ProviderJMAP && active.Mail != nil && active.Mail.JMAP != nil:
			hostSource = active.Mail.JMAP.SessionURL
		case route.Provider == domain.ProviderIMAPSMTP && active.Mail != nil && active.Mail.IMAPSMTP != nil:
			hostSource = "imaps://" + active.Mail.IMAPSMTP.IMAP.Host
		case route.Provider == domain.ProviderCalDAV && service == application.AuthenticationServiceCalendar &&
			active.Calendar != nil && active.Calendar.CalDAV != nil:
			hostSource = active.Calendar.CalDAV.Endpoint
		case route.Provider == domain.ProviderCalDAV && service == application.AuthenticationServiceTasks &&
			active.Tasks != nil && active.Tasks.CalDAV != nil:
			hostSource = active.Tasks.CalDAV.Endpoint
		default:
			found := false
			for _, consent := range consents {
				if consent.provider != route.Provider || !oauthServiceCovered(consent.services, service) {
					continue
				}
				provider, err := oauthlocal.ProviderFor(consent.provider, consent.services)
				if err != nil {
					return nil, errSignInRouteUnsupported
				}
				route.Kind, hostSource = application.AccountSignInProviderAuthorization, provider.AuthURL
				route.ClientID = consent.route.ClientID
				found = true
				break
			}
			if !found {
				return nil, errSignInRouteUnsupported
			}
		}
		host, err := signInHost(hostSource)
		if err != nil {
			return nil, err
		}
		route.Host = host
		if err := route.Validate(); err != nil {
			return nil, errSignInDestinationUndisplayed
		}
		routes = append(routes, route)
	}
	return routes, nil
}
