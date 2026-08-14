package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Capabilities reports behavior confirmed for one signed-in account. False
// means unavailable or not yet confirmed; adapters must not infer support from
// provider branding alone.
type Capabilities struct {
	Mail             bool   `json:"mail"`
	Calendar         bool   `json:"calendar"`
	Tasks            bool   `json:"tasks"`
	Folders          bool   `json:"folders"`
	Labels           bool   `json:"labels"`
	Push             bool   `json:"push"`
	FreeBusy         bool   `json:"freeBusy"`
	OnlineMeeting    string `json:"onlineMeeting,omitempty"`
	IncrementalSync  bool   `json:"incrementalSync"`
	ScheduledSend    bool   `json:"scheduledSend"`
	SharedMailboxes  bool   `json:"sharedMailboxes"`
	SharedCalendars  bool   `json:"sharedCalendars"`
	AttachmentReads  bool   `json:"attachmentReads"`
	AttachmentWrites bool   `json:"attachmentWrites"`
	DraftSend        bool   `json:"draftSend"`
}

// Validate rejects provider-defined open-ended values from the stable contract.
func (capabilities Capabilities) Validate() error {
	switch capabilities.OnlineMeeting {
	case "", "teams", "google-meet", "provider":
		return nil
	default:
		return fmt.Errorf(
			"unsupported online meeting capability %q",
			capabilities.OnlineMeeting,
		)
	}
}

// Degradation makes a provider-specific loss, fallback, or unsupported mapping
// visible rather than silently pretending every provider behaves identically.
type Degradation struct {
	Feature string `json:"feature"`
	Reason  string `json:"reason"`
	Lossy   bool   `json:"lossy"`
}

// Validate keeps degradation reporting bounded and content-free.
func (degradation Degradation) Validate() error {
	if err := validateIdentifier("degradation feature", degradation.Feature, 96); err != nil {
		return err
	}
	if degradation.Reason == "" ||
		len(degradation.Reason) > 512 ||
		strings.TrimSpace(degradation.Reason) != degradation.Reason ||
		strings.ContainsAny(degradation.Reason, "\r\n\x00") {
		return errors.New("degradation reason is malformed")
	}
	return nil
}

// Provenance identifies where a result came from without exposing credentials.
type Provenance struct {
	AccountID      AccountID  `json:"accountId"`
	Provider       ProviderID `json:"provider"`
	MailboxID      string     `json:"mailboxId,omitempty"`
	CalendarID     string     `json:"calendarId,omitempty"`
	TaskListID     string     `json:"taskListId,omitempty"`
	SourceObjectID string     `json:"sourceObjectId,omitempty"`
}

// Validate ensures provenance carries one unambiguous local account boundary.
func (provenance Provenance) Validate() error {
	if err := provenance.AccountID.Validate(); err != nil {
		return err
	}
	if err := provenance.Provider.Validate(); err != nil {
		return err
	}
	selectedContainers := 0
	for _, value := range []string{
		provenance.MailboxID,
		provenance.CalendarID,
		provenance.TaskListID,
	} {
		if value != "" {
			selectedContainers++
		}
	}
	if selectedContainers > 1 {
		return errors.New("provenance cannot name more than one provider container")
	}
	for name, value := range map[string]string{
		"mailbox ID":       provenance.MailboxID,
		"calendar ID":      provenance.CalendarID,
		"task list ID":     provenance.TaskListID,
		"source object ID": provenance.SourceObjectID,
	} {
		if value != "" {
			if err := validateIdentifier(name, value, 4096); err != nil {
				return err
			}
		}
	}
	return nil
}
