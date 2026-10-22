package config

import "slices"

// ServiceEnabled defaults every configured route to on. It grants no authority
// and never creates a route or credentials for an unconfigured service.
func (account Account) ServiceEnabled(service string) bool {
	return !slices.Contains(account.DisabledServices, service)
}

// ActiveServices is a runtime-only projection. Persist the original Account,
// so disabling a service never deletes its configuration or credential handle.
func (account Account) ActiveServices() Account {
	if !account.ServiceEnabled("mail") {
		account.Mail = nil
		account.Monitor = nil
	}
	if !account.ServiceEnabled("calendar") {
		account.Calendar = nil
	}
	if !account.ServiceEnabled("tasks") {
		account.Tasks = nil
	}
	if !account.ServiceEnabled("messages") {
		account.Messages = nil
	}
	return account
}

// ActiveServices excludes disabled routes before authentication or dispatch.
func (configuration Config) ActiveServices() Config {
	accounts := make(map[string]Account, len(configuration.Accounts))
	for alias, account := range configuration.Accounts {
		accounts[alias] = account.ActiveServices()
	}
	configuration.Accounts = accounts
	return configuration
}
