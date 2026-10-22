package main

import (
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/config"
)

func TestAllDisabledAccountCannotStartAuthentication(t *testing.T) {
	configuration := config.OutlookDefault()
	account := configuration.Accounts["work"]
	account.DisabledServices = []string{"mail", "calendar"}
	configuration.Accounts["work"] = account
	backend := &sessionBackend{configuration: configuration}
	if _, err := backend.activateAccount(t.Context(), account.ID); err == nil || !strings.Contains(err.Error(), "all services") {
		t.Fatalf("login = %v", err)
	}
	active, _, err := releasedSessionRoutes(account)
	if err != nil || active.Mail != nil || active.Calendar != nil {
		t.Fatalf("disabled routes active: %+v, %v", active, err)
	}
}
