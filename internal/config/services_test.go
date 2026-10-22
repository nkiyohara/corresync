package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestDisabledServicesProjectWithoutDeletingRoutes(t *testing.T) {
	configuration := OutlookDefault()
	original := configuration.Accounts["work"]
	original.DisabledServices = []string{"mail", "calendar"}
	configuration.Accounts["work"] = original
	active := configuration.ActiveServices().Accounts["work"]
	if active.Mail != nil || active.Calendar != nil || active.Monitor != nil {
		t.Fatal("disabled route remained active")
	}
	if configuration.Accounts["work"].Mail == nil || configuration.Accounts["work"].Calendar == nil {
		t.Fatal("projection mutated persisted routes")
	}
	original.DisabledServices = nil
	if !reflect.DeepEqual(original.ActiveServices(), original) {
		t.Fatal("configured services are not enabled by default")
	}
}

func TestV11MigrationPreservesRoutesAndRejectsPrematureSwitches(t *testing.T) {
	configuration := OutlookDefault()
	configuration.Version = 11
	encoded, err := toml.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := MigrateV11(encoded)
	if err != nil || migrated.Version != CurrentVersion || len(migrated.Accounts["work"].DisabledServices) != 0 {
		t.Fatalf("migration = %+v, %v", migrated, err)
	}
	configuration.Version = CurrentVersion
	if !reflect.DeepEqual(configuration, migrated) {
		t.Fatal("migration changed account configuration")
	}
	if _, err := MigrateV11([]byte(strings.Replace(string(encoded), "[accounts.work]", "[accounts.work]\ndisabled_services = [\"mail\"]", 1))); err == nil {
		t.Fatal("accepted v12 field in v11")
	}
}
