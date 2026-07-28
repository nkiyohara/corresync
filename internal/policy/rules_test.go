package policy

import (
	"testing"

	"github.com/nkiyohara/corresync/internal/domain"
)

func TestRulesEvaluate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rules   Rules
		effect  domain.Effect
		verdict Verdict
	}{
		{"guarded read", DefaultRules(), domain.EffectRead, VerdictAllow},
		{"guarded sensitive read", DefaultRules(), domain.EffectSensitiveRead, VerdictAllow},
		{"guarded reversible", DefaultRules(), domain.EffectReversibleWrite, VerdictAllow},
		{"guarded external", DefaultRules(), domain.EffectExternalWrite, VerdictPreview},
		{"guarded destructive", DefaultRules(), domain.EffectDestructiveWrite, VerdictPreview},
		{"strict sensitive read", Rules{Mode: ModeGuarded, PreviewSensitiveReads: true}, domain.EffectSensitiveRead, VerdictPreview},
		{"strict reversible", Rules{Mode: ModeGuarded, PreviewReversibleWrites: true}, domain.EffectReversibleWrite, VerdictPreview},
		{"read-only read", Rules{Mode: ModeReadOnly}, domain.EffectRead, VerdictAllow},
		{"read-only sensitive", Rules{Mode: ModeReadOnly}, domain.EffectSensitiveRead, VerdictAllow},
		{"read-only reversible", Rules{Mode: ModeReadOnly}, domain.EffectReversibleWrite, VerdictDeny},
		{"read-only external", Rules{Mode: ModeReadOnly}, domain.EffectExternalWrite, VerdictDeny},
		{"read-only destructive", Rules{Mode: ModeReadOnly}, domain.EffectDestructiveWrite, VerdictDeny},
		{"invalid policy", Rules{Mode: "anything"}, domain.EffectRead, VerdictDeny},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			operation, err := domain.NewOperation("test.operation", test.effect, "work", nil)
			if err != nil {
				t.Fatalf("NewOperation() error = %v", err)
			}
			if got := test.rules.Evaluate(operation); got.Verdict != test.verdict {
				t.Fatalf("Evaluate() = %+v, want verdict %q", got, test.verdict)
			}
		})
	}
}
