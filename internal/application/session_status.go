package application

import (
	"time"

	"github.com/nkiyohara/corresync/internal/domain"
)

// SessionStatus contains only local routing metadata, observed capabilities,
// and in-memory authentication freshness. It never exposes remote identities,
// authorization material, or provider session state.
type SessionStatus struct {
	Account          domain.AccountID              `json:"account"`
	Alias            string                        `json:"alias"`
	Provider         domain.ProviderID             `json:"provider"`
	MailProvider     domain.ProviderID             `json:"mailProvider,omitempty"`
	CalendarProvider domain.ProviderID             `json:"calendarProvider,omitempty"`
	TaskProvider     domain.ProviderID             `json:"taskProvider,omitempty"`
	State            string                        `json:"state"`
	Authenticated    bool                          `json:"authenticated"`
	Services         ServiceAuthenticationStatuses `json:"services"`
	CapturedAt       *time.Time                    `json:"capturedAt,omitempty"`
	Capabilities     *domain.Capabilities          `json:"capabilities,omitempty"`
	Degradations     []domain.Degradation          `json:"degradations,omitempty"`
}

// SessionStatusResult reports every configured account in stable alias order.
type SessionStatusResult struct {
	Accounts []SessionStatus `json:"accounts"`
}
