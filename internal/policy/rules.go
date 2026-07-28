// Package policy makes deterministic authorization decisions from effect data.
package policy

import (
	"fmt"

	"github.com/nkiyohara/corresync/internal/domain"
)

// Mode selects the maximum authority available to callers.
type Mode string

const (
	ModeReadOnly Mode = "read_only"
	ModeGuarded  Mode = "guarded"
)

// Verdict is the action the application core must take.
type Verdict string

const (
	VerdictAllow   Verdict = "allow"
	VerdictPreview Verdict = "preview"
	VerdictDeny    Verdict = "deny"
)

// Decision records a stable, machine-readable policy result.
type Decision struct {
	Verdict Verdict `json:"verdict"`
	Reason  string  `json:"reason"`
}

// Rules are intentionally small. There is no unguarded-write mode.
type Rules struct {
	Mode                    Mode
	PreviewSensitiveReads   bool
	PreviewReversibleWrites bool
}

// DefaultRules returns the safe interactive policy.
func DefaultRules() Rules {
	return Rules{Mode: ModeGuarded}
}

// Evaluate classifies an operation without side effects or caller state.
func (rules Rules) Evaluate(operation domain.Operation) Decision {
	if err := rules.Validate(); err != nil {
		return Decision{Verdict: VerdictDeny, Reason: "invalid_policy"}
	}

	switch operation.Effect() {
	case domain.EffectRead:
		return Decision{Verdict: VerdictAllow, Reason: "read"}
	case domain.EffectSensitiveRead:
		if rules.PreviewSensitiveReads {
			return Decision{Verdict: VerdictPreview, Reason: "sensitive_read_requires_preview"}
		}
		return Decision{Verdict: VerdictAllow, Reason: "sensitive_read"}
	case domain.EffectReversibleWrite:
		if rules.Mode == ModeReadOnly {
			return Decision{Verdict: VerdictDeny, Reason: "read_only"}
		}
		if rules.PreviewReversibleWrites {
			return Decision{Verdict: VerdictPreview, Reason: "reversible_write_requires_preview"}
		}
		return Decision{Verdict: VerdictAllow, Reason: "reversible_write"}
	case domain.EffectExternalWrite:
		if rules.Mode == ModeReadOnly {
			return Decision{Verdict: VerdictDeny, Reason: "read_only"}
		}
		return Decision{Verdict: VerdictPreview, Reason: "external_write_requires_preview"}
	case domain.EffectDestructiveWrite:
		if rules.Mode == ModeReadOnly {
			return Decision{Verdict: VerdictDeny, Reason: "read_only"}
		}
		return Decision{Verdict: VerdictPreview, Reason: "destructive_write_requires_preview"}
	default:
		return Decision{Verdict: VerdictDeny, Reason: "unknown_effect"}
	}
}

// Validate rejects unknown or unsafe policy modes.
func (rules Rules) Validate() error {
	switch rules.Mode {
	case ModeReadOnly, ModeGuarded:
		return nil
	default:
		return fmt.Errorf("unknown policy mode %q", rules.Mode)
	}
}
