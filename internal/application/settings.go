package application

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nkiyohara/corresync/internal/approval"
)

const (
	SettingDefaultAccount = "default_account"
	SettingUpdateChannel  = "updates.channel"
	SettingUpdateChecks   = "updates.automatic_checks"
	SettingUpdateInstall  = "updates.auto_install"
	SettingSafetyMode     = "policy.mode"
	SettingLoginTimeout   = "browser.login_timeout"
)

// SettingsAccount is the bounded account information needed by settings UIs.
type SettingsAccount struct {
	Alias     string `json:"alias"`
	Address   string `json:"address,omitempty"`
	IsDefault bool   `json:"isDefault"`
}

// SettingsView is the secret-free everyday configuration shared by CLI and MCP.
type SettingsView struct {
	Accounts           []SettingsAccount `json:"accounts"`
	DefaultAccount     string            `json:"defaultAccount,omitempty"`
	UpdateChannel      string            `json:"updateChannel"`
	AutomaticChecks    bool              `json:"automaticChecks"`
	AutomaticInstall   bool              `json:"automaticInstall"`
	FeedbackAutoSubmit bool              `json:"feedbackAutoSubmit"`
	SafetyMode         string            `json:"safetyMode"`
	LoginTimeout       string            `json:"loginTimeout"`
}

// SettingsUpdateInput changes one well-known everyday setting.
type SettingsUpdateInput struct {
	Key   string `json:"key" jsonschema:"Setting key: default_account, updates.channel, updates.automatic_checks, updates.auto_install, policy.mode, or browser.login_timeout"`
	Value string `json:"value" jsonschema:"New value; booleans accept true/false or on/off, and login timeout accepts Go durations such as 5m"`
}

// SettingsValueChange makes automatic dependent changes explicit in reviews.
type SettingsValueChange struct {
	Key      string `json:"key"`
	Previous string `json:"previous"`
	Value    string `json:"value"`
}

// SettingsChangeReview is an optimistic, approval-bindable settings mutation.
type SettingsChangeReview struct {
	Key              string                `json:"key"`
	Previous         string                `json:"previous"`
	Value            string                `json:"value"`
	Description      string                `json:"description"`
	Command          string                `json:"command"`
	RelatedChanges   []SettingsValueChange `json:"relatedChanges,omitempty"`
	RestartsSessions bool                  `json:"restartsSessions"`
}

// SettingsChangeAccess is either a preview or the resulting view.
type SettingsChangeAccess struct {
	Status   string                `json:"status"`
	Review   *SettingsChangeReview `json:"review,omitempty"`
	Preview  *approval.Preview     `json:"preview,omitempty"`
	Settings *SettingsView         `json:"settings,omitempty"`
}

// SettingsRepository atomically persists validated everyday settings.
type SettingsRepository interface {
	GetSettings(context.Context) (SettingsView, error)
	UpdateSettings(context.Context, SettingsChangeReview) error
}

// SettingsService owns friendly names, normalization, and dependency rules.
type SettingsService struct {
	repository SettingsRepository
}

func NewSettingsService(repository SettingsRepository) (*SettingsService, error) {
	if repository == nil {
		return nil, errors.New("settings repository is required")
	}
	return &SettingsService{repository: repository}, nil
}

func (service *SettingsService) Show(ctx context.Context) (SettingsView, error) {
	return service.repository.GetSettings(ctx)
}

func (service *SettingsService) Review(
	ctx context.Context,
	input SettingsUpdateInput,
) (SettingsChangeReview, error) {
	settings, err := service.Show(ctx)
	if err != nil {
		return SettingsChangeReview{}, err
	}
	key := strings.TrimSpace(input.Key)
	value, err := normalizeSettingValue(settings, key, input.Value)
	if err != nil {
		return SettingsChangeReview{}, err
	}
	previous, err := SettingsValue(settings, key)
	if err != nil {
		return SettingsChangeReview{}, err
	}
	if previous == value {
		return SettingsChangeReview{}, fmt.Errorf("%s is already %s", key, value)
	}
	review := SettingsChangeReview{
		Key: key, Previous: previous, Value: value,
		Description:      settingDescription(key, value),
		Command:          fmt.Sprintf("corr config set %s %s", key, value),
		RestartsSessions: true,
	}
	if key == SettingUpdateInstall && value == "true" && !settings.AutomaticChecks {
		review.RelatedChanges = []SettingsValueChange{{
			Key: SettingUpdateChecks, Previous: "false", Value: "true",
		}}
	}
	return review, nil
}

func (service *SettingsService) Apply(
	ctx context.Context,
	review SettingsChangeReview,
) (SettingsView, error) {
	if review.Key == "" || review.Previous == "" || review.Value == "" {
		return SettingsView{}, errors.New("settings review is incomplete")
	}
	settings, err := service.Show(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	normalized, err := normalizeSettingValue(settings, review.Key, review.Value)
	if err != nil {
		return SettingsView{}, err
	}
	if normalized != review.Value {
		return SettingsView{}, errors.New("settings review contains a non-canonical value")
	}
	if err := service.repository.UpdateSettings(ctx, review); err != nil {
		return SettingsView{}, err
	}
	return service.Show(ctx)
}

// SettingsValue returns the canonical string form used for optimistic writes.
func SettingsValue(settings SettingsView, key string) (string, error) {
	switch key {
	case SettingDefaultAccount:
		return settings.DefaultAccount, nil
	case SettingUpdateChannel:
		return settings.UpdateChannel, nil
	case SettingUpdateChecks:
		return strconv.FormatBool(settings.AutomaticChecks), nil
	case SettingUpdateInstall:
		return strconv.FormatBool(settings.AutomaticInstall), nil
	case SettingSafetyMode:
		return settings.SafetyMode, nil
	case SettingLoginTimeout:
		return settings.LoginTimeout, nil
	default:
		return "", fmt.Errorf("unsupported everyday setting %q", key)
	}
}

func normalizeSettingValue(settings SettingsView, key, raw string) (string, error) {
	value := strings.TrimSpace(raw)
	switch key {
	case SettingDefaultAccount:
		for _, account := range settings.Accounts {
			if account.Alias == value {
				return value, nil
			}
		}
		return "", fmt.Errorf("account %q is not configured", value)
	case SettingUpdateChannel:
		if value != "stable" && value != "preview" {
			return "", errors.New("update channel must be stable or preview")
		}
		return value, nil
	case SettingUpdateChecks, SettingUpdateInstall:
		parsed, err := friendlyBool(value)
		if err != nil {
			return "", fmt.Errorf("%s: %w", key, err)
		}
		if key == SettingUpdateChecks && !parsed && settings.AutomaticInstall {
			return "", errors.New("automatic checks are required while automatic install is on")
		}
		return strconv.FormatBool(parsed), nil
	case SettingSafetyMode:
		if value != "guarded" && value != "read_only" {
			return "", errors.New("safety mode must be guarded or read_only")
		}
		return value, nil
	case SettingLoginTimeout:
		duration, err := time.ParseDuration(value)
		if err != nil {
			return "", fmt.Errorf("parse login timeout as duration: %w", err)
		}
		if duration < time.Minute || duration > 30*time.Minute {
			return "", errors.New("login timeout must be between 1 minute and 30 minutes")
		}
		return duration.String(), nil
	default:
		return "", fmt.Errorf("unsupported everyday setting %q", key)
	}
}

func friendlyBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "true", "on", "yes", "enabled":
		return true, nil
	case "false", "off", "no", "disabled":
		return false, nil
	default:
		return false, errors.New("value must be true/false or on/off")
	}
}

func settingDescription(key, value string) string {
	switch key {
	case SettingDefaultAccount:
		return "Use " + value + " when a command omits --account."
	case SettingUpdateChannel:
		if value == "preview" {
			return "Include alpha, beta, and release-candidate updates."
		}
		return "Offer stable releases only."
	case SettingUpdateChecks:
		if value == "true" {
			return "Check quietly for updates at most once per day."
		}
		return "Never check for updates automatically."
	case SettingUpdateInstall:
		if value == "true" {
			return "Install verified direct updates automatically."
		}
		return "Require an explicit corr update command to install."
	case SettingSafetyMode:
		if value == "read_only" {
			return "Block all writes, including MCP write tools."
		}
		return "Allow writes only through the normal safety and approval policy."
	case SettingLoginTimeout:
		return "Allow browser sign-in to wait up to " + value + "."
	default:
		return "Update one everyday setting."
	}
}
