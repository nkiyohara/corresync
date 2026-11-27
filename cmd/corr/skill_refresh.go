package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/nkiyohara/corresync/internal/agenthost"
	"github.com/nkiyohara/corresync/internal/integrationlifecycle"
	"github.com/nkiyohara/corresync/internal/paths"
)

func (app *runtime) offerSkillUpdates() {
	if !app.interactiveInput() || !app.interactiveStdout() || !app.interactiveOutput() || releaseChannel(app.info.Version) == "" {
		return
	}
	state, err := paths.StateDir()
	if err != nil {
		return
	}
	for _, scope := range []agenthost.Scope{agenthost.ScopeUser, agenthost.ScopeProject, agenthost.ScopeWorkspace, agenthost.ScopeLocal} {
		flags := integrationTargetFlags{Name: "corresync", Scope: string(scope)}
		for _, host := range app.agentHosts.Hosts() {
			if host.Capabilities.AgentSkill && host.Lifecycle.Repair && slices.Contains(host.Scopes, scope) {
				flags.Hosts = append(flags.Hosts, string(host.ID))
			}
		}
		if len(flags.Hosts) == 0 {
			continue
		}
		engine, requests, err := prepareIntegrationLifecycle(app, flags, integrationlifecycle.OperationRepair, false)
		if err != nil || engine.Environment.BundleFiles == nil {
			continue
		}
		for _, request := range requests {
			if !engine.HasManagedSkill(request) {
				continue
			}
			key := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%s\n%s\n%s", app.info.Version, request.Host, scope, request.ProjectDirectory, request.Arguments)))
			marker := filepath.Join(state, "skill-updates", hex.EncodeToString(key[:]))
			if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) { // #nosec G703 -- private state directory plus a fixed-size digest.
				continue
			}
			plan, err := engine.Plan(app.context, request)
			if err != nil || (!plan.SkillUpgrade() && plan.Previous.State != integrationlifecycle.StateHealthy) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil { // #nosec G703 -- private state directory plus a fixed-size digest.
				continue
			}
			if err := os.Mkdir(marker, 0o700); err != nil { // #nosec G703 -- private state directory plus a fixed-size digest.
				continue
			}
			if !plan.SkillUpgrade() {
				continue
			}
			_, _ = fmt.Fprintf(app.stdout, "\nAn updated Corresync Skill is available for %s.\n", plan.DisplayName)
			if err := writeIntegrationPlans(app, []integrationlifecycle.Plan{plan}, true); err != nil {
				return
			}
			accept, err := confirmIntegrationPlans(app, request.Operation, 1)
			if err != nil || !accept {
				continue
			}
			result, err := engine.Apply(app.context, request, plan)
			if err != nil {
				_, _ = fmt.Fprintf(app.stderr, "Skill update for %s: %v\n", plan.DisplayName, err)
				continue
			}
			_ = writeIntegrationResults(app, []integrationlifecycle.Result{result})
		}
	}
}
