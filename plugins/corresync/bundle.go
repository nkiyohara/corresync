// Package corresyncplugin supplies the release's integration assets even when
// the user updates only the standalone executable.
package corresyncplugin

import (
	_ "embed"

	"github.com/nkiyohara/corresync/internal/integrationbundle"
)

//go:embed skills/corresync/SKILL.md
var skill []byte

//go:embed assets/icon.svg
var icon []byte

//go:embed skills/corresync/agents/openai.yaml
var agent []byte

func ReleaseFiles(version string) (map[string][]byte, error) {
	outputs, err := integrationbundle.Render(version, integrationbundle.Inputs{Skill: skill, Icon: icon})
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{integrationbundle.SkillPath: skill, integrationbundle.IconPath: icon,
		"plugins/corresync/skills/corresync/agents/openai.yaml": agent}
	for _, output := range outputs {
		files[output.Path] = output.Data
	}
	return files, nil
}
