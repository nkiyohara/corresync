// Package outlookweb implements the isolated Outlook Web protocol adapter.
package outlookweb

import "github.com/nkiyohara/corresync/internal/domain"

// Action is a closed enum. Arbitrary strings cannot become OWA actions.
type Action uint8

const (
	FindFolder Action = iota + 1
	FindItem
	GetItem
	GetCalendarView
	GetUserAvailabilityInternal
	CreateItem
	CreateCalendarEvent
	UpdateItem
	UpdateCalendarEvent
	MoveItem
	DeleteItem
	CreateAttachment
	GetAttachment
	SendItem
)

type actionSpec struct {
	name   string
	effect domain.Effect
}

var actionSpecs = map[Action]actionSpec{
	FindFolder:                  {name: "FindFolder", effect: domain.EffectRead},
	FindItem:                    {name: "FindItem", effect: domain.EffectRead},
	GetItem:                     {name: "GetItem", effect: domain.EffectSensitiveRead},
	GetCalendarView:             {name: "GetCalendarView", effect: domain.EffectRead},
	GetUserAvailabilityInternal: {name: "GetUserAvailabilityInternal", effect: domain.EffectRead},
	CreateItem:                  {name: "CreateItem", effect: domain.EffectExternalWrite},
	CreateCalendarEvent:         {name: "CreateCalendarEvent", effect: domain.EffectExternalWrite},
	UpdateItem:                  {name: "UpdateItem", effect: domain.EffectExternalWrite},
	UpdateCalendarEvent:         {name: "UpdateCalendarEvent", effect: domain.EffectExternalWrite},
	MoveItem:                    {name: "MoveItem", effect: domain.EffectReversibleWrite},
	DeleteItem:                  {name: "DeleteItem", effect: domain.EffectDestructiveWrite},
	CreateAttachment:            {name: "CreateAttachment", effect: domain.EffectExternalWrite},
	GetAttachment:               {name: "GetAttachment", effect: domain.EffectSensitiveRead},
	SendItem:                    {name: "SendItem", effect: domain.EffectExternalWrite},
}

// Name returns the protocol action name or an empty string for invalid values.
func (action Action) Name() string { return actionSpecs[action].name }

// Effect returns the protocol action's conservative retry classification.
// Application policy separately classifies the typed use case: for example,
// CreateItem SaveOnly is a reversible write but still must not be retried.
func (action Action) Effect() domain.Effect { return actionSpecs[action].effect }

func (action Action) valid() bool {
	_, exists := actionSpecs[action]
	return exists
}
