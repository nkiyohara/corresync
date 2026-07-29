package main

import (
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/config"
	"github.com/nkiyohara/corresync/internal/domain"
)

func TestValidateMonitorNotificationPlatform(t *testing.T) {
	t.Parallel()
	if err := validateMonitorNotificationPlatform(
		domain.MonitorNotify,
		"windows",
	); err == nil {
		t.Fatal("notify mode unexpectedly accepted unavailable Windows adapter")
	}
	if err := validateMonitorNotificationPlatform(
		domain.MonitorQueue,
		"windows",
	); err != nil {
		t.Fatalf("queue mode inherited notification restriction: %v", err)
	}
	if err := validateMonitorNotificationPlatform(
		domain.MonitorNotify,
		"linux",
	); err != nil {
		t.Fatalf("notify mode rejected Linux: %v", err)
	}
}

func TestMonitorEnableAndDisableAreExplicitAndAccountScoped(t *testing.T) {
	app, path, _ := newAccountCommandRuntime(t, nil)
	configuration, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	second := configuration.Accounts["work"]
	second.ID = "acc_00000000000000000000000000000002"
	configuration.Accounts["personal"] = second
	if err := config.Save(path, configuration); err != nil {
		t.Fatal(err)
	}

	enable := &monitorEnableCommand{
		Account: "work", Mode: "queue",
		PollInterval: time.Minute, Debounce: 30 * time.Second,
		Retention: 30 * 24 * time.Hour, RateLimitHour: 30,
		RunnerEgress: "local", RunnerTimeout: 2 * time.Minute,
	}
	if err := enable.Run(app); err == nil {
		t.Fatal("monitor enable succeeded without explicit approval")
	}
	enable.Approve = true
	if err := enable.Run(app); err == nil {
		t.Fatal("monitor enable skipped the notify consent boundary")
	}
	enable.Mode = "notify"
	if err := enable.Run(app); err != nil {
		t.Fatalf("notify enable error = %v", err)
	}
	enable.Mode = "queue"
	if err := enable.Run(app); err != nil {
		t.Fatalf("queue enable error = %v", err)
	}
	enabled, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.Accounts["work"].Monitor == nil ||
		enabled.Accounts["work"].Monitor.Mode != domain.MonitorQueue {
		t.Fatalf("work monitor = %+v", enabled.Accounts["work"].Monitor)
	}
	if enabled.Accounts["personal"].Monitor != nil {
		t.Fatal("enabling work also enabled personal")
	}

	disable := &monitorDisableCommand{Account: "work", Approve: true}
	if err := disable.Run(app); err == nil {
		t.Fatal("monitor disable accepted no retain-or-purge choice")
	}
	disable.RetainQueue = true
	if err := disable.Run(app); err != nil {
		t.Fatalf("monitor disable error = %v", err)
	}
	disabled, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if disabled.Accounts["work"].Monitor != nil {
		t.Fatal("monitor remained enabled")
	}
}

func TestAgentModeRequiresSeparateRemoteEgressApproval(t *testing.T) {
	app, _, _ := newAccountCommandRuntime(t, nil)
	for _, mode := range []string{"notify", "queue"} {
		command := monitorEnableCommand{
			Account: "work", Mode: mode, Approve: true,
			PollInterval: time.Minute, Debounce: 30 * time.Second,
			Retention: 30 * 24 * time.Hour, RateLimitHour: 30,
			RunnerEgress: "local", RunnerTimeout: 2 * time.Minute,
		}
		if err := command.Run(app); err != nil {
			t.Fatalf("enable %s: %v", mode, err)
		}
	}
	command := &monitorEnableCommand{
		Account: "work", Mode: "agent", Approve: true,
		PollInterval: time.Minute, Debounce: 30 * time.Second,
		Retention: 30 * 24 * time.Hour, RateLimitHour: 30,
		Runner: "/synthetic/runner", RunnerEgress: "remote",
		RunnerFields:  []string{"account", "event_id", "subject", "trust"},
		RunnerTimeout: 2 * time.Minute,
	}
	if err := command.Run(app); err == nil {
		t.Fatal("agent mode accepted remote egress without separate approval")
	}
	command.ApproveRemoteEgress = true
	if err := command.Run(app); err == nil {
		t.Fatal("initial agent enable also enabled remote egress")
	}
	command.RunnerEgress = "local"
	command.ApproveRemoteEgress = false
	if err := command.Run(app); err != nil {
		t.Fatalf("local agent mode error = %v", err)
	}
	command.RunnerEgress = "remote"
	if err := command.Run(app); err == nil {
		t.Fatal("agent reconfiguration accepted remote egress without approval")
	}
	command.ApproveRemoteEgress = true
	if err := command.Run(app); err != nil {
		t.Fatalf("approved agent mode error = %v", err)
	}
}
