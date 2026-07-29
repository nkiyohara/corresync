package domain

import "fmt"

// MonitorMode is an ordered, account-scoped consent boundary. Advancing to a
// later mode grants only the behavior named by that mode.
type MonitorMode string

const (
	MonitorOff    MonitorMode = "off"
	MonitorNotify MonitorMode = "notify"
	MonitorQueue  MonitorMode = "queue"
	MonitorAgent  MonitorMode = "agent"
)

// Validate rejects modes unknown to this version of the application.
func (mode MonitorMode) Validate() error {
	switch mode {
	case MonitorOff, MonitorNotify, MonitorQueue, MonitorAgent:
		return nil
	default:
		return fmt.Errorf("unknown monitor mode %q", mode)
	}
}

// Collects reports whether the provider watcher may read metadata.
func (mode MonitorMode) Collects() bool { return mode != MonitorOff }

// Queues reports whether matching metadata may be persisted in the local
// outbox. Notification delivery also uses the outbox so deferrals never rewind
// the provider cursor or drop a first-seen event.
func (mode MonitorMode) Queues() bool {
	return mode != MonitorOff
}

// Dispatches reports whether an explicitly configured runner may be invoked.
func (mode MonitorMode) Dispatches() bool { return mode == MonitorAgent }
