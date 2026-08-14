package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
)

const (
	maximumPayloadBytes = 4 << 20
	// A composite target may contain two independently bounded 4096-byte
	// provider IDs plus a four-digit length and separator.
	maximumTargetIDBytes = 2*4096 + 5
)

var operationNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._][a-z0-9]+)*$`)

// Operation is an immutable, normalized request evaluated by the policy core.
type Operation struct {
	name    string
	effect  Effect
	account AccountID
	target  TargetRef
	payload json.RawMessage
}

// TargetKind identifies the exact writable object collection selected by a
// preview. A commit token is bound to this target independently of payload
// serialization.
type TargetKind string

const (
	TargetMailbox      TargetKind = "mailbox"
	TargetCalendar     TargetKind = "calendar"
	TargetTaskList     TargetKind = "task_list"
	TargetTask         TargetKind = "task"
	TargetWorkspace    TargetKind = "workspace"
	TargetConversation TargetKind = "conversation"
	TargetMessage      TargetKind = "message"
	TargetLocalQueue   TargetKind = "local_queue"
)

// TargetRef is an immutable mutation-routing boundary.
type TargetRef struct {
	Kind TargetKind `json:"kind"`
	ID   string     `json:"id"`
}

// Validate rejects open-ended or ambiguous target references.
func (target TargetRef) Validate() error {
	maximum := 4096
	switch target.Kind {
	case TargetTask, TargetConversation:
		maximum = maximumTargetIDBytes
	case TargetMessage:
		maximum = 3*4096 + 16
	case TargetMailbox, TargetCalendar, TargetTaskList, TargetWorkspace, TargetLocalQueue:
	default:
		return fmt.Errorf("invalid target kind %q", target.Kind)
	}
	return validateIdentifier("target ID", target.ID, maximum)
}

// OperationView is the non-secret metadata safe to return in a preview.
type OperationView struct {
	Name    string     `json:"name"`
	Effect  Effect     `json:"effect"`
	Account AccountID  `json:"account"`
	Target  *TargetRef `json:"target,omitempty"`
	Digest  string     `json:"digest"`
}

// NewOperation validates and snapshots a typed operation payload.
func NewOperation(name string, effect Effect, account AccountID, payload any) (Operation, error) {
	return newOperation(name, effect, account, TargetRef{}, payload)
}

// NewTargetedOperation binds a write preview to one exact mailbox, calendar,
// or local queue target in addition to the account and immutable payload.
func NewTargetedOperation(
	name string,
	effect Effect,
	account AccountID,
	target TargetRef,
	payload any,
) (Operation, error) {
	if err := target.Validate(); err != nil {
		return Operation{}, err
	}
	return newOperation(name, effect, account, target, payload)
}

func newOperation(
	name string,
	effect Effect,
	account AccountID,
	target TargetRef,
	payload any,
) (Operation, error) {
	if !operationNamePattern.MatchString(name) || len(name) > 96 {
		return Operation{}, fmt.Errorf("invalid operation name %q", name)
	}
	if err := effect.Validate(); err != nil {
		return Operation{}, err
	}
	if err := account.Validate(); err != nil {
		return Operation{}, err
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return Operation{}, fmt.Errorf("encode operation payload: %w", err)
	}
	if len(encoded) > maximumPayloadBytes {
		return Operation{}, fmt.Errorf("operation payload exceeds %d bytes", maximumPayloadBytes)
	}

	return Operation{
		name:    name,
		effect:  effect,
		account: account,
		target:  target,
		payload: encoded,
	}, nil
}

// Name returns the stable operation name.
func (operation Operation) Name() string {
	return operation.name
}

// Effect returns the operation's effect class.
func (operation Operation) Effect() Effect {
	return operation.effect
}

// Account returns the mailbox account boundary for the operation.
func (operation Operation) Account() AccountID {
	return operation.account
}

// DecodePayload decodes the immutable payload into a typed destination.
func (operation Operation) DecodePayload(destination any) error {
	if err := json.Unmarshal(operation.payload, destination); err != nil {
		return fmt.Errorf("decode operation payload: %w", err)
	}
	return nil
}

// View returns metadata and a digest without exposing operation content.
func (operation Operation) View() OperationView {
	digest := operation.digest()
	view := OperationView{
		Name:    operation.name,
		Effect:  operation.effect,
		Account: operation.account,
		Digest:  hex.EncodeToString(digest[:]),
	}
	if operation.target.Kind != "" {
		target := operation.target
		view.Target = &target
	}
	return view
}

// Validate rejects fabricated or incomplete operation metadata.
func (view OperationView) Validate() error {
	if !operationNamePattern.MatchString(view.Name) || len(view.Name) > 96 {
		return fmt.Errorf("invalid operation view name %q", view.Name)
	}
	if err := view.Effect.Validate(); err != nil {
		return err
	}
	if err := view.Account.Validate(); err != nil {
		return err
	}
	if view.Target != nil {
		if err := view.Target.Validate(); err != nil {
			return err
		}
	}
	if len(view.Digest) != 2*sha256.Size {
		return errors.New("operation view digest must be a SHA-256 hex string")
	}
	if _, err := hex.DecodeString(view.Digest); err != nil {
		return fmt.Errorf("decode operation view digest: %w", err)
	}
	return nil
}

func (operation Operation) digest() [sha256.Size]byte {
	encoded, err := json.Marshal(struct {
		Version int             `json:"version"`
		Name    string          `json:"name"`
		Effect  Effect          `json:"effect"`
		Account AccountID       `json:"account"`
		Target  TargetRef       `json:"target,omitempty"`
		Payload json.RawMessage `json:"payload"`
	}{
		Version: 2,
		Name:    operation.name,
		Effect:  operation.effect,
		Account: operation.account,
		Target:  operation.target,
		Payload: operation.payload,
	})
	if err != nil {
		panic("domain operation contains invalid normalized JSON: " + err.Error())
	}
	return sha256.Sum256(encoded)
}
