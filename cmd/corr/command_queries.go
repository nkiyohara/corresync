package main

import (
	"errors"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/savedquerystore"
)

type queriesCommand struct {
	List   savedQueryListCommand   `cmd:"" help:"List private definitions without running them."`
	Show   savedQueryShowCommand   `cmd:"" help:"Show one private definition without running it."`
	Save   savedQuerySaveCommand   `cmd:"" help:"Review and save one bounded private definition."`
	Run    savedQueryRunCommand    `cmd:"" help:"Run one definition against its live provider."`
	Delete savedQueryDeleteCommand `cmd:"" help:"Review and delete one private definition."`
	Purge  savedQueryPurgeCommand  `cmd:"" help:"Review and purge this account's private catalog."`
}

type savedQueryListCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type savedQueryShowCommand struct {
	Name    string `arg:"" help:"Saved query name."`
	Account string `help:"Configured account alias; defaults to default_account."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type savedQuerySaveCommand struct {
	Mail     savedMailQueryCommand     `cmd:"" help:"Save a bounded provider-native mail search."`
	Calendar savedCalendarQueryCommand `cmd:"" help:"Save a relative live calendar window."`
}

type savedMailQueryCommand struct {
	Name     string `arg:"" help:"Stable name using letters, digits, dots, dashes, or underscores."`
	Query    string `arg:"" help:"Private query in the selected mail provider's user-facing language."`
	Account  string `help:"Configured account alias; defaults to default_account."`
	Folder   string `default:"inbox" enum:"inbox,archive,deleteditems,drafts,sentitems" help:"Well-known folder."`
	FolderID string `name:"folder-id" help:"Opaque folder ID from mail folder discovery; takes precedence over folder."`
	Limit    int    `default:"25" help:"Messages to return per live run (1-50)."`
	TimeZone string `name:"time-zone" default:"UTC" help:"Provider time-zone identifier."`
	Approve  bool   `help:"Apply the exact reviewed private definition."`
	JSON     bool   `help:"Write the stable machine-readable schema."`
}

type savedCalendarQueryCommand struct {
	Name        string        `arg:"" help:"Stable name using letters, digits, dots, dashes, or underscores."`
	Account     string        `help:"Configured account alias; defaults to default_account."`
	CalendarID  string        `name:"calendar-id" help:"Opaque calendar ID from calendar discovery; omit for the default calendar."`
	StartOffset time.Duration `name:"start-offset" default:"0s" help:"Relative start from each live run, in whole minutes (up to 31 days)."`
	Window      time.Duration `default:"168h" help:"Live calendar window in whole minutes (1 minute through 31 days)."`
	TimeZone    string        `name:"time-zone" default:"UTC" help:"IANA display time zone recorded with the result."`
	Approve     bool          `help:"Apply the exact reviewed private definition."`
	JSON        bool          `help:"Write the stable machine-readable schema."`
}

type savedQueryRunCommand struct {
	Name    string `arg:"" help:"Saved query name."`
	Account string `help:"Configured account alias; defaults to default_account."`
	Offset  int    `default:"0" help:"Zero-based live provider page offset (0-10000)."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type savedQueryDeleteCommand struct {
	Name    string `arg:"" help:"Saved query name."`
	Account string `help:"Configured account alias; defaults to default_account."`
	Approve bool   `help:"Delete the exact private definition shown in the review."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

type savedQueryPurgeCommand struct {
	Account string `help:"Configured account alias; defaults to default_account."`
	Approve bool   `help:"Permanently delete the exact account-local catalog shown in the review."`
	JSON    bool   `help:"Write the stable machine-readable schema."`
}

func (command *savedQueryListCommand) Run(app *runtime) error {
	account, err := resolveSavedQueryAccount(app, command.Account)
	if err != nil {
		return err
	}
	service, err := application.NewSavedQueryService(savedquerystore.New(), nil)
	if err != nil {
		return err
	}
	catalog, err := service.List(app.context, account)
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, catalog)
	}
	if len(catalog.Queries) == 0 {
		_, err = fmt.Fprintln(app.stdout, "No saved queries for this account.")
		return err
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tKIND\tCONTENT CACHE\tREVISION"); err != nil {
		return err
	}
	for _, query := range catalog.Queries {
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\tnone\t%s\n",
			sanitizeCell(query.Name, application.MaxSavedQueryNameBytes),
			query.Kind,
			query.Revision[:12],
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (command *savedQueryShowCommand) Run(app *runtime) error {
	account, err := resolveSavedQueryAccount(app, command.Account)
	if err != nil {
		return err
	}
	service, err := application.NewSavedQueryService(savedquerystore.New(), nil)
	if err != nil {
		return err
	}
	query, err := service.Get(app.context, application.SavedQueryDeleteInput{
		Account: account,
		Name:    command.Name,
	})
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, query)
	}
	return writeSavedQueryDefinition(app, query)
}

func (command *savedMailQueryCommand) Run(app *runtime) error {
	account, err := resolveSavedQueryAccount(app, command.Account)
	if err != nil {
		return err
	}
	folder := application.MailFolder{
		Kind: application.MailFolderDistinguished,
		ID:   command.Folder,
	}
	if command.FolderID != "" {
		folder = application.MailFolder{Kind: application.MailFolderOpaque, ID: command.FolderID}
	}
	input := application.SavedQuerySaveInput{
		Account: account, Name: command.Name, Kind: application.SavedQueryMail,
		Mail: &application.SavedMailQuery{
			Folder: folder, Query: command.Query,
			Limit: command.Limit, TimeZone: command.TimeZone,
		},
	}
	return saveQuery(app, input, command.Approve, command.JSON)
}

func (command *savedCalendarQueryCommand) Run(app *runtime) error {
	startOffset, err := wholeMinutes("start offset", command.StartOffset)
	if err != nil {
		return err
	}
	window, err := wholeMinutes("window", command.Window)
	if err != nil {
		return err
	}
	account, err := resolveSavedQueryAccount(app, command.Account)
	if err != nil {
		return err
	}
	calendar := application.CalendarFolder{
		Kind: application.CalendarFolderDistinguished,
		ID:   "calendar",
	}
	if command.CalendarID != "" {
		calendar = application.CalendarFolder{
			Kind: application.CalendarFolderOpaque,
			ID:   command.CalendarID,
		}
	}
	input := application.SavedQuerySaveInput{
		Account: account, Name: command.Name, Kind: application.SavedQueryCalendar,
		Calendar: &application.SavedCalendarQuery{
			Calendar: calendar, StartOffsetMinutes: startOffset,
			WindowMinutes: window, DisplayTimeZone: command.TimeZone,
		},
	}
	return saveQuery(app, input, command.Approve, command.JSON)
}

func saveQuery(
	app *runtime,
	input application.SavedQuerySaveInput,
	approve bool,
	jsonOutput bool,
) error {
	service, err := application.NewSavedQueryService(savedquerystore.New(), nil)
	if err != nil {
		return err
	}
	review, err := service.ReviewSave(app.context, input)
	if err != nil {
		return err
	}
	if !approve {
		access := application.SavedQueryChangeAccess{
			Status: "approval_required", Review: &review,
		}
		if jsonOutput {
			return writeJSON(app.stdout, access)
		}
		return writeSavedQueryChangeReview(app, review, false)
	}
	query, err := service.ApplySave(app.context, review)
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(app.stdout, application.SavedQueryChangeAccess{
			Status: "completed", Query: &query,
		})
	}
	return writeSavedQueryChangeReview(app, review, true)
}

func (command *savedQueryRunCommand) Run(app *runtime) (returnErr error) {
	account, err := resolveSavedQueryAccount(app, command.Account)
	if err != nil {
		return err
	}
	client, _, err := app.openDaemon(app.context)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, client.Close()) }()
	service, err := application.NewSavedQueryService(savedquerystore.New(), client)
	if err != nil {
		return err
	}
	result, err := service.Run(app.context, application.SavedQueryRunInput{
		Account: account, Name: command.Name, Offset: command.Offset,
	}, app.caller())
	if err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	if _, err := fmt.Fprintf(
		app.stdout,
		"Live provider result · fetched %s · no content cache\n\n",
		result.FetchedAt.Format(time.RFC3339),
	); err != nil {
		return err
	}
	if result.Mail != nil {
		return writeMailTable(app, *result.Mail)
	}
	if result.Calendar != nil {
		return writeCalendarTable(app, *result.Calendar)
	}
	return errors.New("saved query returned no typed result")
}

func (command *savedQueryDeleteCommand) Run(app *runtime) error {
	account, err := resolveSavedQueryAccount(app, command.Account)
	if err != nil {
		return err
	}
	service, err := application.NewSavedQueryService(savedquerystore.New(), nil)
	if err != nil {
		return err
	}
	review, err := service.ReviewDelete(app.context, application.SavedQueryDeleteInput{
		Account: account, Name: command.Name,
	})
	if err != nil {
		return err
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, application.SavedQueryChangeAccess{
				Status: "approval_required", Review: &review,
			})
		}
		return writeSavedQueryChangeReview(app, review, false)
	}
	if err := service.ApplyDelete(app.context, review); err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, application.SavedQueryChangeAccess{Status: "completed"})
	}
	return writeSavedQueryChangeReview(app, review, true)
}

func (command *savedQueryPurgeCommand) Run(app *runtime) error {
	account, err := resolveSavedQueryAccount(app, command.Account)
	if err != nil {
		return err
	}
	service, err := application.NewSavedQueryService(savedquerystore.New(), nil)
	if err != nil {
		return err
	}
	review, err := service.ReviewPurge(app.context, application.SavedQueryPurgeInput{
		Account: account,
	})
	if err != nil {
		return err
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, application.SavedQueryPurgeAccess{
				Status: "approval_required", Review: &review,
			})
		}
		return writeSavedQueryPurgeReview(app, review, false)
	}
	if err := service.ApplyPurge(app.context, review); err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, application.SavedQueryPurgeAccess{
			Status: "completed", Purged: true,
		})
	}
	return writeSavedQueryPurgeReview(app, review, true)
}

func resolveSavedQueryAccount(app *runtime, reference string) (domain.AccountID, error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return "", err
	}
	return app.account(configuration, reference)
}

func wholeMinutes(name string, value time.Duration) (int, error) {
	if value%time.Minute != 0 {
		return 0, fmt.Errorf("saved query %s must use whole minutes", name)
	}
	minutes := value / time.Minute
	if int64(int(minutes)) != int64(minutes) {
		return 0, fmt.Errorf("saved query %s is outside the supported range", name)
	}
	return int(minutes), nil
}

func writeSavedQueryDefinition(
	app *runtime,
	query application.SavedQueryDefinition,
) error {
	if _, err := fmt.Fprintf(
		app.stdout,
		"%s · %s\nAccount: %s\nRevision: %s\nContent cache: none\n",
		query.Name,
		query.Kind,
		query.Account,
		query.Revision,
	); err != nil {
		return err
	}
	if query.Mail != nil {
		_, err := fmt.Fprintf(
			app.stdout,
			"Folder: %s:%s\nLimit: %d\nTime zone: %s\nPrivate query: %q\n",
			query.Mail.Folder.Kind,
			sanitizeCell(query.Mail.Folder.ID, 4096),
			query.Mail.Limit,
			sanitizeCell(query.Mail.TimeZone, 128),
			query.Mail.Query,
		)
		return err
	}
	_, err := fmt.Fprintf(
		app.stdout,
		"Calendar: %s:%s\nStart offset: %d minutes\nWindow: %d minutes\nDisplay time zone: %s\n",
		query.Calendar.Calendar.Kind,
		sanitizeCell(query.Calendar.Calendar.ID, 4096),
		query.Calendar.StartOffsetMinutes,
		query.Calendar.WindowMinutes,
		sanitizeCell(query.Calendar.DisplayTimeZone, 128),
	)
	return err
}

func writeSavedQueryChangeReview(
	app *runtime,
	review application.SavedQueryChangeReview,
	completed bool,
) error {
	verb := "Save"
	if review.Action == "delete" {
		verb = "Delete"
	}
	status := "No changes made. Re-run with --approve to apply this exact review."
	if completed {
		status = verb + "d. Provider content was not cached."
	}
	if _, err := fmt.Fprintf(
		app.stdout,
		"%s private saved query %q for %s\nKind: %s\nReplaces existing: %t\nRevision: %s\n%s\n",
		verb,
		review.Name,
		review.Account,
		review.Kind,
		review.Replaces,
		review.Definition.Revision,
		status,
	); err != nil {
		return err
	}
	if !completed && review.Action == "save" {
		return writeSavedQueryDefinition(app, review.Definition)
	}
	return nil
}

func writeSavedQueryPurgeReview(
	app *runtime,
	review application.SavedQueryPurgeReview,
	completed bool,
) error {
	status := "No changes made. Re-run with --approve to purge this exact catalog."
	if completed {
		status = "Purged the private catalog. Provider content was not stored or removed."
	}
	_, err := fmt.Fprintf(
		app.stdout,
		"Purge private saved queries for %s\nDefinitions: %d\nCorrupt catalog: %t\nCatalog revision: %s\n%s\n",
		review.Account,
		review.Definitions,
		review.Corrupt,
		review.CatalogRevision,
		status,
	)
	return err
}
