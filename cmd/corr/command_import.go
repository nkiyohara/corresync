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
	Scan  importScanCommand  `cmd:"" help:"Read a local export into a private staging area on this computer."`
	Purge importPurgeCommand `cmd:"" help:"Delete an account's staged imports; the source is not touched."`
}

func (*importCommand) Help() string {
	return `Import reads a mail or calendar export you already have on this computer,
such as an mbox file, a Maildir folder, or a Thunderbird profile, and stages
its contents in a private area of the account's local state. It only reads
the source: it never signs in, uploads, or changes the source. Nothing is
copied to your mailbox.

Examples:
  corr import scan ~/Downloads/archive.mbox --account work
  corr import scan ~/Downloads/archive.mbox --account work --approve-read
  corr import purge --account work --approve`
}

type importScanCommand struct {
	Source      string `arg:"" name:"source" type:"path" help:"Path of the archive, export file, Maildir folder, or Thunderbird profile to read."`
	Account     string `help:"Account whose staging area to use (alias); defaults to the default account."`
	Format      string `enum:"auto,mixed,mbox,maildir,eml,ics,vcf,pst,olm,thunderbird" default:"auto" help:"Format of the source; auto works it out from the path you give."`
	ApproveRead bool   `name:"approve-read" help:"Allow reading this one source; it is only read, and nothing signs in or uploads."`
	JSON        bool   `help:"Write the staging plan as machine-readable JSON."`
}

type importPurgeCommand struct {
	Account string `help:"Account whose staged imports to delete (alias); defaults to the default account."`
	Approve bool   `help:"Confirm deleting this account's staged imports."`
	JSON    bool   `help:"Write machine-readable JSON."`
}

func (command *importScanCommand) Run(app *runtime) error {
	if !command.ApproveRead {
		retry := []string{"corr", "import", "scan", command.Source}
		if command.Account != "" {
			retry = append(retry, "--account", command.Account)
		}
		if command.Format != "" && command.Format != "auto" {
			retry = append(retry, "--format", command.Format)
		}
		return withHint(errors.New(importPrivacyExplanation(
			goruntime.GOOS,
			command.Source,
		)+". Nothing was read yet; check the path, then add --approve-read"), shellCommand(append(retry, "--approve-read")...))
	}
	source, err := filepath.Abs(command.Source)
	if err != nil {
		return fmt.Errorf("resolve import source: %w", err)
	}
	source = filepath.Clean(source)
	if _, err := fmt.Fprintln(
		app.stderr,
		importPrivacyExplanation(goruntime.GOOS, source)+".",
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
		view.strong("Staged the export for import (read only)"),
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
			view.muted("This source was staged before with the same result; nothing was staged twice."),
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
		retry := []string{"corr", "import", "purge"}
		if command.Account != "" {
			retry = append(retry, "--account", command.Account)
		}
		return withHint(errors.New(
			"import purge deletes the staged copies Corresync made, never the source; add --approve to confirm",
		), shellCommand(append(retry, "--approve")...))
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
		view.strong("Deleted this account's staged imports"),
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
		"Reading %q may need %s. Corresync only reads it: it does not read saved passwords or other sign-in data, sign in, upload, or change, move, or delete the source",
		source,
		permission,
	)
}
