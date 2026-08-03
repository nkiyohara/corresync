// Package settingsstore persists the everyday settings application contract.
package settingsstore

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/policy"
)

type Store struct {
	ConfigPath string
}

func (store Store) GetSettings(ctx context.Context) (application.SettingsView, error) {
	if err := ctx.Err(); err != nil {
		return application.SettingsView{}, err
	}
	configuration, err := config.Load(store.ConfigPath)
	if err != nil {
		return application.SettingsView{}, err
	}
	return project(configuration), nil
}

func (store Store) UpdateSettings(
	ctx context.Context,
	review application.SettingsChangeReview,
) error {
	return config.Update(ctx, store.ConfigPath, func(configuration *config.Config) error {
		current, err := application.SettingsValue(project(*configuration), review.Key)
		if err != nil {
			return err
		}
		if current != review.Previous {
			return fmt.Errorf(
				"%s changed after review: expected %s, found %s; review it again",
				review.Key, review.Previous, current,
			)
		}
		switch review.Key {
		case application.SettingDefaultAccount:
			configuration.DefaultAccount = review.Value
		case application.SettingUpdateChannel:
			configuration.Updates.Channel = config.UpdateChannel(review.Value)
		case application.SettingUpdateChecks:
			enabled, err := strconv.ParseBool(review.Value)
			if err != nil {
				return fmt.Errorf("parse automatic checks: %w", err)
			}
			configuration.Updates.DisableAutomaticChecks = !enabled
		case application.SettingUpdateInstall:
			enabled, err := strconv.ParseBool(review.Value)
			if err != nil {
				return fmt.Errorf("parse automatic install: %w", err)
			}
			configuration.Updates.AutoInstall = enabled
			if enabled {
				configuration.Updates.DisableAutomaticChecks = false
			}
		case application.SettingSafetyMode:
			configuration.Policy.Mode = policy.Mode(review.Value)
		case application.SettingLoginTimeout:
			duration, err := time.ParseDuration(review.Value)
			if err != nil {
				return fmt.Errorf("parse login timeout: %w", err)
			}
			configuration.Browser.LoginTimeout = config.Duration(duration)
		default:
			return fmt.Errorf("unsupported everyday setting %q", review.Key)
		}
		return nil
	})
}

func project(configuration config.Config) application.SettingsView {
	aliases := make([]string, 0, len(configuration.Accounts))
	for alias := range configuration.Accounts {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	accounts := make([]application.SettingsAccount, 0, len(aliases))
	for _, alias := range aliases {
		accounts = append(accounts, application.SettingsAccount{
			Alias: alias, Address: configuration.Accounts[alias].Address,
			IsDefault: alias == configuration.DefaultAccount,
		})
	}
	return application.SettingsView{
		Accounts: accounts, DefaultAccount: configuration.DefaultAccount,
		UpdateChannel:    string(configuration.Updates.Channel),
		AutomaticChecks:  !configuration.Updates.DisableAutomaticChecks,
		AutomaticInstall: configuration.Updates.AutoInstall,
		SafetyMode:       string(configuration.Policy.Mode),
		LoginTimeout:     time.Duration(configuration.Browser.LoginTimeout).String(),
	}
}
