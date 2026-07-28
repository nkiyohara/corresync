package importstage

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
	"github.com/nkiyohara/corresync/internal/paths"
)

const importTestAccount domain.AccountID = "acc_00000000000000000000000000000001"

func TestMBOXScanRetainsMetadataAndIsIdempotent(t *testing.T) {
	state := t.TempDir()
	t.Setenv("CORRESYNC_STATE_DIR", state)
	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "archive.mbox")
	archive := []byte(
		"From sender@example.test Mon Jan  1 00:00:00 2024\n" +
			"Message-ID: <one@example.test>\n" +
			"Date: Mon, 01 Jan 2024 00:00:00 +0000\n" +
			"Status: R\n" +
			"X-Status: FA\n" +
			"Subject: Synthetic one\n" +
			"\n" +
			"body one\n" +
			">From escaped body line\n" +
			"From sender@example.test Tue Jan  2 00:00:00 2024\n" +
			"Message-ID: <two@example.test>\n" +
			"Date: Tue, 02 Jan 2024 01:02:03 +0000\n" +
			"Subject: Synthetic two\n" +
			"\n" +
			"body two\n",
	)
	writeFixture(t, source, archive)

	scanner := New()
	first := scanFixture(t, scanner, source, application.ImportFormatMBOX)
	if first.StagedItems != 2 || first.DuplicateItems != 0 ||
		first.ExistingPlan || first.BytesRead != int64(len(archive)) {
		t.Fatalf("unexpected first scan: %+v", first)
	}
	if len(first.Items) != 2 ||
		first.Items[0].MessageID != "<one@example.test>" ||
		first.Items[0].OriginalDate != "2024-01-01T00:00:00Z" ||
		!equalStrings(first.Items[0].Flags, []string{"answered", "flagged", "seen"}) ||
		first.Items[0].Folder != "archive" ||
		first.Items[0].Source.Path != source ||
		first.Items[0].Source.Ordinal != 1 {
		t.Fatalf("MBOX metadata was not retained: %+v", first.Items)
	}
	if !hasDegradation(first.Items[0].Degradations, "import.mbox_from_escaping") {
		t.Fatalf("MBOX normalization loss was not reported: %+v", first.Items[0])
	}

	rawObject := readObject(t, first.Items[0].ObjectSHA256)
	if !bytes.Contains(rawObject, []byte("\nFrom escaped body line\n")) ||
		bytes.Contains(rawObject, []byte("\n>From escaped body line\n")) {
		t.Fatalf("unexpected staged raw MIME: %q", rawObject)
	}

	second := scanFixture(t, scanner, source, application.ImportFormatMBOX)
	if second.ID != first.ID || !second.ExistingPlan ||
		second.StagedItems != 0 || second.DuplicateItems != 2 {
		t.Fatalf("scan was not idempotent: first=%+v second=%+v", first, second)
	}
	if got := countObjectFiles(t); got != 2 {
		t.Fatalf("object files = %d, want 2", got)
	}
	after, err := os.ReadFile(source) // #nosec G304 -- source is a test-owned fixture path.
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, archive) {
		t.Fatal("read-only scan changed the source archive")
	}

	accountState, err := paths.AccountStateDir(importTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(accountState, "keep.txt")
	writeFixture(t, sibling, []byte("account state outside imports"))
	if err := scanner.Purge(context.Background(), importTestAccount); err != nil {
		t.Fatalf("Purge() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(accountState, "imports")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("import staging still exists: %v", err)
	}
	// #nosec G304 -- sibling is a test-owned fixture path.
	if content, err := os.ReadFile(sibling); err != nil ||
		string(content) != "account state outside imports" {
		t.Fatalf("purge changed sibling account state: %q, %v", content, err)
	}
	if err := scanner.Purge(context.Background(), importTestAccount); err != nil {
		t.Fatalf("second Purge() error = %v", err)
	}
}

func TestCalendarAndContactScansRetainIdentityAndConflicts(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	sourceRoot := t.TempDir()
	calendarPath := filepath.Join(sourceRoot, "calendar.ics")
	firstCalendar := []byte(
		"BEGIN:VCALENDAR\r\n" +
			"VERSION:2.0\r\n" +
			"BEGIN:VEVENT\r\n" +
			"UID:event-1@example.test\r\n" +
			"RECURRENCE-ID:20240101T120000Z\r\n" +
			"SUMMARY:First synthetic event\r\n" +
			"BEGIN:VALARM\r\nACTION:DISPLAY\r\nEND:VALARM\r\n" +
			"END:VEVENT\r\n" +
			"END:VCALENDAR\r\n",
	)
	writeFixture(t, calendarPath, firstCalendar)

	scanner := New()
	first := scanFixture(t, scanner, calendarPath, application.ImportFormatICS)
	if len(first.Items) != 1 ||
		first.Items[0].CalendarUID != "event-1@example.test" ||
		first.Items[0].RecurrenceID != "20240101T120000Z" ||
		first.Items[0].Status != "staged" {
		t.Fatalf("calendar identity was not retained: %+v", first)
	}

	secondCalendar := bytes.Replace(
		firstCalendar,
		[]byte("First synthetic event"),
		[]byte("Changed synthetic event"),
		1,
	)
	writeFixture(t, calendarPath, secondCalendar)
	second := scanFixture(t, scanner, calendarPath, application.ImportFormatICS)
	if second.Conflicts != 1 || second.StagedItems != 1 ||
		len(second.Items) != 1 || second.Items[0].Status != "conflict" ||
		second.Items[0].ObjectSHA256 == first.Items[0].ObjectSHA256 ||
		!hasDegradation(second.Items[0].Degradations, "import.deduplication") {
		t.Fatalf("same-UID conflict was not retained and reported: %+v", second)
	}

	contactsPath := filepath.Join(sourceRoot, "contacts.vcf")
	writeFixture(t, contactsPath, []byte(
		"BEGIN:VCARD\r\nVERSION:4.0\r\n"+
			"UID:contact-1@example.test\r\nFN:Synthetic Person\r\nEND:VCARD\r\n",
	))
	contacts := scanFixture(t, scanner, contactsPath, application.ImportFormatVCF)
	if len(contacts.Items) != 1 ||
		contacts.Items[0].Kind != "contact" ||
		contacts.Items[0].ContactUID != "contact-1@example.test" {
		t.Fatalf("contact UID was not retained: %+v", contacts)
	}
}

func TestMaildirAndEMLScansRetainRawMessagesAndFlags(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	sourceRoot := t.TempDir()
	maildir := filepath.Join(sourceRoot, "Maildir")
	message := []byte(
		"Message-ID: <maildir@example.test>\r\n" +
			"Date: Wed, 03 Jan 2024 04:05:06 +0000\r\n" +
			"Subject: Synthetic Maildir message\r\n\r\nbody\r\n",
	)
	maildirPath := filepath.Join(maildir, "cur", "1700000000.synthetic:2,SFR")
	writeFixture(t, maildirPath, message)
	if err := os.MkdirAll(filepath.Join(maildir, "new"), 0o700); err != nil {
		t.Fatal(err)
	}

	scanner := New()
	maildirPlan := scanFixture(
		t,
		scanner,
		maildir,
		application.ImportFormatAuto,
	)
	if maildirPlan.Format != application.ImportFormatMaildir ||
		len(maildirPlan.Items) != 1 ||
		maildirPlan.Items[0].Source.Path != maildirPath ||
		maildirPlan.Items[0].Source.Format != application.ImportFormatMaildir ||
		!equalStrings(
			maildirPlan.Items[0].Flags,
			[]string{"answered", "flagged", "seen"},
		) ||
		!bytes.Equal(readObject(t, maildirPlan.Items[0].ObjectSHA256), message) {
		t.Fatalf("Maildir data was not retained: %+v", maildirPlan)
	}

	emlPath := filepath.Join(sourceRoot, "single.eml")
	eml := []byte(
		"Message-ID: <eml@example.test>\n" +
			"Date: deliberately malformed\n" +
			"Subject: Synthetic EML message\n\nbody\n",
	)
	writeFixture(t, emlPath, eml)
	emlPlan := scanFixture(t, scanner, emlPath, application.ImportFormatAuto)
	if emlPlan.Format != application.ImportFormatEML ||
		len(emlPlan.Items) != 1 ||
		emlPlan.Items[0].MessageID != "<eml@example.test>" ||
		emlPlan.Items[0].OriginalDate != "" ||
		!hasDegradation(emlPlan.Items[0].Degradations, "import.original_date") ||
		!bytes.Equal(readObject(t, emlPlan.Items[0].ObjectSHA256), eml) {
		t.Fatalf("EML data or loss reporting was not retained: %+v", emlPlan)
	}
}

func TestProprietaryArchivesStopAtDecisionGatesWithoutReadingContent(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	sourceRoot := t.TempDir()
	scanner := New()
	for _, format := range []application.ImportFormat{
		application.ImportFormatPST,
		application.ImportFormatOLM,
	} {
		path := filepath.Join(sourceRoot, "archive."+string(format))
		writeFixture(t, path, []byte("SECRET_MARKER_MUST_NOT_BE_STAGED"))
		plan := scanFixture(t, scanner, path, format)
		if plan.BytesRead != 0 || len(plan.Items) != 0 ||
			len(plan.DecisionGates) != 1 ||
			plan.DecisionGates[0].Format != format {
			t.Fatalf("%s did not stop at its decision gate: %+v", format, plan)
		}
	}
	assertStagingExcludes(t, []byte("SECRET_MARKER_MUST_NOT_BE_STAGED"))
}

func TestThunderbirdScanOnlyStagesSanitizedAccountHints(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	root := t.TempDir()
	profile := filepath.Join(root, "Profiles", "synthetic.default")
	writeFixture(t, filepath.Join(root, "profiles.ini"), []byte(
		"[Profile0]\nName=synthetic\nIsRelative=1\nPath=Profiles/synthetic.default\n",
	))
	writeFixture(t, filepath.Join(profile, "prefs.js"), []byte(
		"user_pref(\"mail.server.server1.type\", \"imap\");\n"+
			"user_pref(\"mail.server.server1.hostname\", \"mail.example.test\");\n"+
			"user_pref(\"mail.server.server1.userName\", \"person@example.test\");\n"+
			"user_pref(\"mail.server.server1.password\", \"SECRET_PREF_MARKER\");\n",
	))
	writeFixture(t, filepath.Join(profile, "logins.json"), []byte(
		`{"encryptedPassword":"SECRET_LOGIN_MARKER"}`,
	))

	plan := scanFixture(t, New(), root, application.ImportFormatThunderbird)
	if len(plan.Items) != 0 || len(plan.DesktopHints) != 1 {
		t.Fatalf("unexpected Thunderbird plan: %+v", plan)
	}
	hint := plan.DesktopHints[0]
	if hint.Application != "thunderbird" || hint.AccountType != "imap" ||
		hint.Host != "mail.example.test" ||
		hint.Identity != "person@example.test" {
		t.Fatalf("unexpected Thunderbird hint: %+v", hint)
	}
	if !hasDegradation(plan.Degradations, "import.desktop_credentials") ||
		!hasDegradation(plan.Degradations, "import.imap_cache") {
		t.Fatalf("credential/cache exclusions were not reported: %+v", plan.Degradations)
	}
	assertStagingExcludes(t, []byte("SECRET_PREF_MARKER"))
	assertStagingExcludes(t, []byte("SECRET_LOGIN_MARKER"))
}

func TestScanRejectsSymbolicLinkSource(t *testing.T) {
	t.Setenv("CORRESYNC_STATE_DIR", t.TempDir())
	root := t.TempDir()
	target := filepath.Join(root, "message.eml")
	link := filepath.Join(root, "linked.eml")
	writeFixture(t, target, []byte("Message-ID: <link@example.test>\n\nbody\n"))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	_, err := New().Scan(context.Background(), application.ImportScanInput{
		Account: importTestAccount, Source: link,
		Format: application.ImportFormatEML, PrivacyApproved: true,
	})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("Scan() error = %v", err)
	}
}

func scanFixture(
	t *testing.T,
	scanner *Scanner,
	source string,
	format application.ImportFormat,
) application.ImportPlan {
	t.Helper()
	plan, err := scanner.Scan(context.Background(), application.ImportScanInput{
		Account: importTestAccount, Source: source,
		Format: format, PrivacyApproved: true,
	})
	if err != nil {
		t.Fatalf("Scan(%s) error = %v", format, err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("plan validation error = %v", err)
	}
	return plan
}

func writeFixture(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readObject(t *testing.T, digest string) []byte {
	t.Helper()
	accountState, err := paths.AccountStateDir(importTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(accountState, "imports", "objects", digest[:2], digest)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("object mode = %o, want 600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path) // #nosec G304 -- path is derived from test-owned state.
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func countObjectFiles(t *testing.T) int {
	t.Helper()
	accountState, err := paths.AccountStateDir(importTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(accountState, "imports", "objects")
	count := 0
	if err := filepath.WalkDir(root, func(
		_ string,
		entry fs.DirEntry,
		err error,
	) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertStagingExcludes(t *testing.T, forbidden []byte) {
	t.Helper()
	accountState, err := paths.AccountStateDir(importTestAccount)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(accountState, "imports")
	if err := filepath.WalkDir(root, func(
		path string,
		entry fs.DirEntry,
		err error,
	) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		// #nosec G304,G122 -- WalkDir limits path to the immutable test-owned staging root.
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(content, forbidden) {
			t.Fatalf("staging file %s contains forbidden source content", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func hasDegradation(values []domain.Degradation, feature string) bool {
	for _, value := range values {
		if value.Feature == feature {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
