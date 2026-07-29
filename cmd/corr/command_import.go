package main

import (
	"errors"
	"fmt"
	"path/filepath"
	goruntime "runtime"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/importstage"
)

type importCommand struct {
	Scan  importScanCommand  `cmd:"" help:"Read one explicit local source and create an upload-free staging plan."`
	Purge importPurgeCommand `cmd:"" help:"Delete one account's Corresync-owned import staging."`
}

type importScanCommand struct {
	Source      string `arg:"" name:"source" type:"path" help:"Explicit archive, export, Maildir, or Thunderbird profile path."`
	Account     string `help:"Configured account alias that owns the local staging area; defaults to default_account."`
	Format      string `enum:"auto,mixed,mbox,maildir,eml,ics,vcf,pst,olm,thunderbird" default:"auto" help:"Source format; auto uses only the explicit path."`
	ApproveRead bool   `name:"approve-read" help:"Approve read-only access to this exact local source; never approves authentication or upload."`
	JSON        bool   `help:"Write the stable machine-readable plan."`
}

type importPurgeCommand struct {
	Account string `help:"Configured account alias whose import staging is removed; defaults to default_account."`
	Approve bool   `help:"Approve deletion of this account's Corresync-owned staged imports."`
	JSON    bool   `help:"Write machine-readable output."`
}

func (command *importScanCommand) Run(app *runtime) error {
	if !command.ApproveRead {
		return errors.New(importPrivacyExplanation(
			goruntime.GOOS,
			command.Source,
		) + "; review the path and rerun with --approve-read")
	}
	source, err := filepath.Abs(command.Source)
	if err != nil {
		return fmt.Errorf("resolve import source: %w", err)
	}
	source = filepath.Clean(source)
	if _, err := fmt.Fprintln(
		app.stderr,
		importPrivacyExplanation(goruntime.GOOS, source),
	); err != nil {
		return err
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	service, err := application.NewImportService(importstage.New())
	if err != nil {
		return err
	}
	plan, err := service.Scan(app.context, application.ImportScanInput{
		Account: accountID, Source: source,
		Format:          application.ImportFormat(command.Format),
		PrivacyApproved: true,
	})
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, plan)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	if _, err := view.printf(
		"%s  %s\n\n  %-16s %s\n  %-16s %s\n  %-16s %d\n  %-16s %d\n  %-16s %d\n  %-16s %d\n",
		view.success(),
		view.strong("Read-only import plan staged"),
		"Plan", plan.ID,
		"Format", plan.Format,
		"Staged", plan.StagedItems,
		"Duplicates", plan.DuplicateItems,
		"Conflicts", plan.Conflicts,
		"Bytes read", plan.BytesRead,
	); err != nil {
		return err
	}
	if plan.ExistingPlan {
		if _, err := view.printf(
			"  %s\n",
			view.muted("The deterministic plan already existed; no duplicate objects were created."),
		); err != nil {
			return err
		}
	}
	for _, gate := range plan.DecisionGates {
		if _, err := view.printf(
			"  %s  %s: %s\n",
			view.warning(),
			gate.Format,
			sanitizeCell(gate.Reason, 512),
		); err != nil {
			return err
		}
	}
	for _, degradation := range plan.Degradations {
		if _, err := view.printf(
			"  %s  %s: %s\n",
			view.warning(),
			sanitizeCell(degradation.Feature, 96),
			sanitizeCell(degradation.Reason, 512),
		); err != nil {
			return err
		}
	}
	for _, hint := range plan.DesktopHints {
		if _, err := view.printf(
			"  %s  %s · %s · %s\n",
			view.info(),
			sanitizeCell(hint.Application, 64),
			sanitizeCell(hint.AccountType, 64),
			sanitizeCell(hint.Host, 253),
		); err != nil {
			return err
		}
	}
	return nil
}

func (command *importPurgeCommand) Run(app *runtime) error {
	if !command.Approve {
		return errors.New(
			"import purge deletes only Corresync-owned staged content; rerun with --approve",
		)
	}
	configuration, _, err := app.loadConfig()
	if err != nil {
		return err
	}
	accountID, err := app.account(configuration, command.Account)
	if err != nil {
		return err
	}
	service, err := application.NewImportService(importstage.New())
	if err != nil {
		return err
	}
	if err := service.Purge(app.context, accountID); err != nil {
		return err
	}
	result := map[string]any{
		"purged": true, "account": accountID,
	}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err = view.printf(
		"%s  %s\n",
		view.success(),
		view.strong("Purged account-owned import staging"),
	)
	return err
}

func importPrivacyExplanation(goos, source string) string {
	permission := "filesystem permissions"
	switch goos {
	case "darwin":
		permission = "macOS Files and Folders or Full Disk Access"
	case "windows":
		permission = "Windows user-profile filesystem access"
	case "linux":
		permission = "Linux filesystem or sandbox portal access"
	}
	return fmt.Sprintf(
		"Read-only import may request %s to read %q; it will not read credential stores, authenticate, upload, mutate, move, or delete the source",
		permission,
		source,
	)
}
