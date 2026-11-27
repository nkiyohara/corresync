package integrationlifecycle

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
)

// HasManagedSkill checks only managed locations before invoking any host CLI.
func (engine Engine) HasManagedSkill(request Request) bool {
	_, descriptor, ok, err := resolveNativeAdapter(engine.Environment, request)
	if err == nil && ok {
		_, err = os.Lstat(filepath.Join(descriptor.targetRoot, packageMarkerName))
		return err == nil
	}
	skill, ok, err := resolveSkillDescriptor(engine.Environment, request)
	if err != nil || !ok {
		return false
	}
	_, err = os.Lstat(filepath.Join(skill.targetRoot, skillMarkerName))
	return err == nil
}

// SkillUpgrade permits only a repair of an installed older Skill. A broader
// repair (including enabling an integration) still needs an explicit request.
func (plan Plan) SkillUpgrade() bool {
	if plan.Operation != OperationRepair || plan.Blocked || len(plan.Actions) == 0 {
		return false
	}
	older := false
	for _, c := range plan.Previous.Components {
		switch c.State {
		case StateHealthy:
		case StateVersionDrift:
			if c.Component != ComponentSkill && c.Component != ComponentPlugin && c.Component != ComponentExtension && c.Component != ComponentStage {
				return false
			}
			oldVersion := "v" + strings.TrimPrefix(c.Version, "v")
			newVersion := "v" + strings.TrimPrefix(c.ExpectedVersion, "v")
			if !semver.IsValid(oldVersion) || !semver.IsValid(newVersion) || semver.Compare(oldVersion, newVersion) > 0 {
				return false
			}
			older = older || semver.Compare(oldVersion, newVersion) < 0
		case StateAbsent, StateDisabled, StateStalePath, StateNameConflict, StateMalformed, StateUnreadable, StateUnavailable:
			return false
		}
	}
	return older
}
