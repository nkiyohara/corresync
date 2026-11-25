package main

import (
	"errors"
	"fmt"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/savedquerystore"
)

type queriesCommand struct {
	List   savedQueryListCommand   `cmd:"" help:"List your saved queries without running them."`
	Show   savedQueryShowCommand   `cmd:"" help:"Show one saved query without running it."`
	Save   savedQuerySaveCommand   `cmd:"" help:"Save a mail search or a calendar time range under a name."`
	Run    savedQueryRunCommand    `cmd:"" help:"Run a saved query and show the live results."`
	Delete savedQueryDeleteCommand `cmd:"" help:"Delete one saved query."`
	Purge  savedQueryPurgeCommand  `cmd:"" help:"Delete all saved queries of an account permanently."`
}

var queriesHelpExamples = []helpExample{
	{"Save a mail search, then run it:", []string{
		"queries", "save", "mail", "from-ana", "from:ana@example.invalid", "--approve",
	}},
	{"", []string{"queries", "run", "from-ana"}},
	{"Save \"the next 7 days\" of your calendar:", []string{
		"queries", "save", "calendar", "next-week", "--window", "168h", "--time-zone", "Europe/London",
	}},
}

// Help is the detail text of `corr queries --help`.
func (queriesCommand) Help() string {
	return helpWithExamples(`
A saved query keeps only the search, never the messages or events it finds;
each run asks the provider again. Queries are private to one account and stay
on this computer.

save, delete, and purge show a preview first and change nothing. Run the same
command again with --approve added to carry it out; the preview ends with that
command.`, queriesHelpExamples)
}

type savedQueryListCommand struct {
	Account string `help:"Account alias (default: your default account)."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

type savedQueryShowCommand struct {
	Name    string `arg:"" help:"Name of the saved query."`
	Account string `help:"Account alias (default: your default account)."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

type savedQuerySaveCommand struct {
	Mail     savedMailQueryCommand     `cmd:"" help:"Save a mail search under a name."`
	Calendar savedCalendarQueryCommand `cmd:"" help:"Save a calendar time range, counted from each run, under a name."`
}

type savedMailQueryCommand struct {
	Name     string `arg:"" help:"Name for the query: letters, digits, dots, dashes, or underscores."`
	Query    string `arg:"" help:"Search words, in your mail provider's search syntax."`
	Account  string `help:"Account alias (default: your default account)."`
	Folder   string `default:"inbox" enum:"inbox,archive,deleteditems,drafts,sentitems" help:"Folder to search, by name."`
	FolderID string `name:"folder-id" placeholder:"ID" help:"Folder to search, by ID from corr mail folders; overrides --folder."`
	Limit    int    `default:"25" help:"Most messages to show per run (1-50)."`
	TimeZone string `name:"time-zone" default:"UTC" help:"Time zone the provider uses for dates."`
	Approve  bool   `help:"Save the query instead of showing a preview."`
	JSON     bool   `help:"Print JSON for scripts and agents."`
}

type savedCalendarQueryCommand struct {
	Name        string        `arg:"" help:"Name for the query: letters, digits, dots, dashes, or underscores."`
	Account     string        `help:"Account alias (default: your default account)."`
	CalendarID  string        `name:"calendar-id" placeholder:"ID" help:"Calendar ID, from corr calendar folders; defaults to your main calendar."`
	StartOffset time.Duration `name:"start-offset" default:"0s" help:"How far after the moment of each run the range starts, in whole minutes (up to 31 days)."`
	Window      time.Duration `default:"168h" help:"Length of the range, in whole minutes (1 minute to 31 days)."`
	TimeZone    string        `name:"time-zone" default:"UTC" help:"Time zone to show results in, such as Europe/London."`
	Approve     bool          `help:"Save the query instead of showing a preview."`
	JSON        bool          `help:"Print JSON for scripts and agents."`
}

type savedQueryRunCommand struct {
	Name    string `arg:"" help:"Name of the saved query."`
	Account string `help:"Account alias (default: your default account)."`
	Offset  int    `default:"0" help:"Skip this many results, for paging (0-10000)."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

type savedQueryDeleteCommand struct {
	Name    string `arg:"" help:"Name of the saved query."`
	Account string `help:"Account alias (default: your default account)."`
	Approve bool   `help:"Delete the query instead of showing a preview."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

type savedQueryPurgeCommand struct {
	Account string `help:"Account alias (default: your default account)."`
	Approve bool   `help:"Delete every saved query of the account permanently instead of showing a preview."`
	JSON    bool   `help:"Print JSON for scripts and agents."`
}

func (command *savedQueryListCommand) Run(app *runtime) error {
	alias, account, err := resolveSavedQueryAccount(app, command.Account)
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
		return app.writeEmpty(emptyResult{
			message: fmt.Sprintf("No saved queries for account %s.", sanitizeCell(alias, 64)),
			next: printableCommand(withAccount(
				[]string{commandName, "queries", "save", "mail", "NAME", "QUERY"}, command.Account,
			)...),
		})
	}
	writer := tabwriter.NewWriter(app.stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "NAME\tKIND\tSTORES RESULTS\tREVISION"); err != nil {
		return err
	}
	for _, query := range catalog.Queries {
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\tno\t%s\n",
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
	_, account, err := resolveSavedQueryAccount(app, command.Account)
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
		return withSavedQueryNameHint(app, service, account, command.Name, command.Account, err)
	}
	if command.JSON {
		return writeJSON(app.stdout, query)
	}
	return writeSavedQueryDefinition(app, query)
}

func (command *savedMailQueryCommand) Run(app *runtime) error {
	_, account, err := resolveSavedQueryAccount(app, command.Account)
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
	return saveQuery(app, input, command.Approve, command.JSON, app.approvalCommand(command, "queries", "save", "mail"))
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
	_, account, err := resolveSavedQueryAccount(app, command.Account)
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
	return saveQuery(app, input, command.Approve, command.JSON, app.approvalCommand(command, "queries", "save", "calendar"))
}

func saveQuery(
	app *runtime,
	input application.SavedQuerySaveInput,
	approve bool,
	jsonOutput bool,
	approvalCommand string,
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
		return writeSavedQueryChangeReview(app, review, false, approvalCommand)
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
	return writeSavedQueryChangeReview(app, review, true, "")
}

func (command *savedQueryRunCommand) Run(app *runtime) (returnErr error) {
	defer func() { returnErr = explainCommandError(returnErr, command.JSON) }()
	_, account, err := resolveSavedQueryAccount(app, command.Account)
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
		return withSavedQueryNameHint(app, service, account, command.Name, command.Account, err)
	}
	if command.JSON {
		return writeJSON(app.stdout, result)
	}
	if _, err := fmt.Fprintf(
		app.stdout,
		"Live results from the provider, fetched %s; results are not stored.\n\n",
		result.FetchedAt.Format(time.RFC3339),
	); err != nil {
		return err
	}
	empty := emptyResult{message: fmt.Sprintf("Saved query %s found nothing.", quoteForSentence(command.Name))}
	if result.Mail != nil {
		return writeMailTable(app, *result.Mail, empty)
	}
	if result.Calendar != nil {
		return writeCalendarTable(app, *result.Calendar, empty)
	}
	return errors.New("saved query returned neither mail nor calendar results")
}

func (command *savedQueryDeleteCommand) Run(app *runtime) error {
	_, account, err := resolveSavedQueryAccount(app, command.Account)
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
		return withSavedQueryNameHint(app, service, account, command.Name, command.Account, err)
	}
	if !command.Approve {
		if command.JSON {
			return writeJSON(app.stdout, application.SavedQueryChangeAccess{
				Status: "approval_required", Review: &review,
			})
		}
		return writeSavedQueryChangeReview(app, review, false, app.approvalCommand(command, "queries", "delete"))
	}
	if err := service.ApplyDelete(app.context, review); err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, application.SavedQueryChangeAccess{Status: "completed"})
	}
	return writeSavedQueryChangeReview(app, review, true, "")
}

func (command *savedQueryPurgeCommand) Run(app *runtime) error {
	_, account, err := resolveSavedQueryAccount(app, command.Account)
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
		return writeSavedQueryPurgeReview(app, review, false, app.approvalCommand(command, "queries", "purge"))
	}
	if err := service.ApplyPurge(app.context, review); err != nil {
		return err
	}
	if command.JSON {
		return writeJSON(app.stdout, application.SavedQueryPurgeAccess{
			Status: "completed", Purged: true,
		})
	}
	return writeSavedQueryPurgeReview(app, review, true, "")
}

func resolveSavedQueryAccount(app *runtime, reference string) (string, domain.AccountID, error) {
	configuration, _, err := app.loadConfig()
	if err != nil {
		return "", "", err
	}
	return resolveAccount(configuration, reference)
}

// withSavedQueryNameHint points to corr queries list when the named query is
// not in the account's catalog.
func withSavedQueryNameHint(
	app *runtime,
	service *application.SavedQueryService,
	account domain.AccountID,
	name, reference string,
	err error,
) error {
	catalog, listErr := service.List(app.context, account)
	if listErr != nil {
		return err
	}
	for _, query := range catalog.Queries {
		if query.Name == name {
			return err
		}
	}
	return withHint(err, printableCommand(withAccount([]string{commandName, "queries", "list"}, reference)...))
}

func wholeMinutes(name string, value time.Duration) (int, error) {
	if value%time.Minute != 0 {
		return 0, withHint(
			fmt.Errorf("saved query %s must use whole minutes", name),
			"Use a whole number of minutes, such as 90m or 24h.",
		)
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
	if _, err := fmt.Fprintf(app.stdout, "Saved query %q (%s)\n", query.Name, query.Kind); err != nil {
		return err
	}
	fields := []reviewField{
		{"Account", sanitizeCell(string(query.Account), 128)},
		{"Revision", sanitizeCell(query.Revision, 128)},
		{"Stores results", "no"},
	}
	if query.Mail != nil {
		fields = append(fields,
			reviewField{"Folder", fmt.Sprintf("%s:%s", query.Mail.Folder.Kind, sanitizeCell(query.Mail.Folder.ID, 4096))},
			reviewField{"Limit", strconv.Itoa(query.Mail.Limit)},
			reviewField{"Time zone", sanitizeCell(query.Mail.TimeZone, 128)},
			reviewField{"Query", fmt.Sprintf("%q", query.Mail.Query)},
		)
		return writeReviewFields(app.stdout, fields)
	}
	fields = append(fields,
		reviewField{"Calendar", fmt.Sprintf(
			"%s:%s", query.Calendar.Calendar.Kind, sanitizeCell(query.Calendar.Calendar.ID, 4096),
		)},
		reviewField{"Starts", fmt.Sprintf("%d minutes after each run", query.Calendar.StartOffsetMinutes)},
		reviewField{"Length", fmt.Sprintf("%d minutes", query.Calendar.WindowMinutes)},
		reviewField{"Time zone", sanitizeCell(query.Calendar.DisplayTimeZone, 128)},
	)
	return writeReviewFields(app.stdout, fields)
}

func writeSavedQueryChangeReview(
	app *runtime,
	review application.SavedQueryChangeReview,
	completed bool,
	approvalCommand string,
) error {
	status := fmt.Sprintf("Preview: saved query %q has not been saved.", review.Name)
	switch {
	case completed && review.Action == "delete":
		status = fmt.Sprintf("Deleted saved query %q. No mail or calendar results were stored.", review.Name)
	case completed:
		status = fmt.Sprintf("Saved query %q. Only the search is stored, not its results.", review.Name)
	case review.Action == "delete":
		status = fmt.Sprintf("Preview: saved query %q has not been deleted.", review.Name)
	}
	if _, err := fmt.Fprintln(app.stdout, status); err != nil {
		return err
	}
	if err := writeReviewFields(app.stdout, []reviewField{
		{"Account", sanitizeCell(string(review.Account), 128)},
		{"Kind", sanitizeCell(string(review.Kind), 32)},
		{"Replaces existing", previewYesNo(review.Replaces)},
		{"Revision", sanitizeCell(review.Definition.Revision, 128)},
	}); err != nil {
		return err
	}
	if !completed && review.Action == "save" {
		if err := writeSavedQueryDefinition(app, review.Definition); err != nil {
			return err
		}
	}
	if completed || approvalCommand == "" {
		return nil
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := fmt.Fprintln(app.stdout, view.nextStep(approvalCommand))
	return err
}

func writeSavedQueryPurgeReview(
	app *runtime,
	review application.SavedQueryPurgeReview,
	completed bool,
	approvalCommand string,
) error {
	status := "Preview: nothing has been deleted. Approving deletes every saved query of this account permanently."
	if completed {
		status = "Deleted every saved query of this account permanently. No mail or calendar results were stored, so none were removed."
	}
	if _, err := fmt.Fprintln(app.stdout, wrapText(status, app.textWidth(app.stdout), "")); err != nil {
		return err
	}
	if err := writeReviewFields(app.stdout, []reviewField{
		{"Account", sanitizeCell(string(review.Account), 128)},
		{"Saved queries", strconv.Itoa(review.Definitions)},
		{"Damaged catalog", previewYesNo(review.Corrupt)},
		{"Catalog revision", sanitizeCell(review.CatalogRevision, 128)},
	}); err != nil {
		return err
	}
	if completed || approvalCommand == "" {
		return nil
	}
	view := newConsoleView(app, app.stdout, app.interactiveStdout())
	_, err := fmt.Fprintln(app.stdout, view.nextStep(approvalCommand))
	return err
}
