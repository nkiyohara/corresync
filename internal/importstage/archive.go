package importstage

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const maximumWalkEntries = 50_000

func (scanner *Scanner) scanSource(
	ctx context.Context,
	source string,
	requested application.ImportFormat,
) (scanResult, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return scanResult{}, fmt.Errorf("inspect import source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return scanResult{}, errors.New("import source must not be a symbolic link")
	}
	format, err := detectFormat(source, info, requested)
	if err != nil {
		return scanResult{}, err
	}
	result := scanResult{format: format}
	switch format {
	case application.ImportFormatPST, application.ImportFormatOLM:
		if !info.Mode().IsRegular() {
			return scanResult{}, errors.New(
				"proprietary archive import source must be a regular file",
			)
		}
		result.gates = append(result.gates, archiveDecisionGate(format))
	case application.ImportFormatMBOX:
		if !info.Mode().IsRegular() {
			return scanResult{}, errors.New("mbox import source must be a regular file")
		}
		candidates, bytesRead, err := scanMBOX(ctx, source)
		if err != nil {
			return scanResult{}, err
		}
		result.candidates = candidates
		result.bytesRead = bytesRead
	case application.ImportFormatMaildir:
		if !info.IsDir() {
			return scanResult{}, errors.New("maildir import source must be a directory")
		}
		candidates, bytesRead, degradations, err := scanMaildir(ctx, source)
		if err != nil {
			return scanResult{}, err
		}
		result.candidates = candidates
		result.bytesRead = bytesRead
		result.degradations = degradations
	case application.ImportFormatEML,
		application.ImportFormatICS,
		application.ImportFormatVCF:
		candidates, bytesRead, err := scanHomogeneous(
			ctx,
			source,
			info,
			format,
		)
		if err != nil {
			return scanResult{}, err
		}
		result.candidates = candidates
		result.bytesRead = bytesRead
	case application.ImportFormatMixed:
		if !info.IsDir() {
			return scanResult{}, errors.New("mixed import source must be a directory")
		}
		result, err = scanMixed(ctx, source)
		if err != nil {
			return scanResult{}, err
		}
	case application.ImportFormatThunderbird:
		if !info.IsDir() && !info.Mode().IsRegular() {
			return scanResult{}, errors.New(
				"thunderbird source must be a profile directory or profiles.ini",
			)
		}
		result, err = scanThunderbird(ctx, source, info)
		if err != nil {
			return scanResult{}, err
		}
	case application.ImportFormatAuto:
		return scanResult{}, errors.New("automatic import format was not resolved")
	default:
		return scanResult{}, fmt.Errorf("unsupported import format %q", format)
	}
	sortCandidates(result.candidates)
	return result, nil
}

func detectFormat(
	source string,
	info os.FileInfo,
	requested application.ImportFormat,
) (application.ImportFormat, error) {
	if requested != application.ImportFormatAuto {
		return requested, nil
	}
	if info.IsDir() {
		if isMaildir(source) {
			return application.ImportFormatMaildir, nil
		}
		if _, err := os.Lstat(filepath.Join(source, "profiles.ini")); err == nil {
			return application.ImportFormatThunderbird, nil
		}
		return application.ImportFormatMixed, nil
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("import source must be a regular file or directory")
	}
	switch strings.ToLower(filepath.Ext(source)) {
	case ".mbox":
		return application.ImportFormatMBOX, nil
	case ".eml":
		return application.ImportFormatEML, nil
	case ".ics":
		return application.ImportFormatICS, nil
	case ".vcf", ".vcard":
		return application.ImportFormatVCF, nil
	case ".pst":
		return application.ImportFormatPST, nil
	case ".olm":
		return application.ImportFormatOLM, nil
	case ".ini":
		if strings.EqualFold(filepath.Base(source), "profiles.ini") {
			return application.ImportFormatThunderbird, nil
		}
	}
	return "", errors.New(
		"cannot infer import format; pass --format with an explicit supported format",
	)
}

func scanHomogeneous(
	ctx context.Context,
	source string,
	info os.FileInfo,
	format application.ImportFormat,
) ([]candidate, int64, error) {
	paths := []string{source}
	if info.IsDir() {
		extension := map[application.ImportFormat][]string{
			application.ImportFormatEML: {".eml"},
			application.ImportFormatICS: {".ics"},
			application.ImportFormatVCF: {".vcf", ".vcard"},
		}[format]
		var err error
		paths, err = collectFiles(ctx, source, func(path string) bool {
			return slices.Contains(extension, strings.ToLower(filepath.Ext(path)))
		})
		if err != nil {
			return nil, 0, err
		}
	}
	result := make([]candidate, 0, len(paths))
	var total int64
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		content, err := readSourceFile(path, application.MaxImportItemBytes)
		if err != nil {
			return nil, 0, err
		}
		total += int64(len(content))
		if total > application.MaxImportSourceBytes {
			return nil, 0, errors.New("import source exceeds the total byte limit")
		}
		switch format {
		case application.ImportFormatEML:
			result = append(result, mailImportCandidate(
				content,
				path,
				1,
				filepath.Base(filepath.Dir(path)),
				nil,
				format,
				nil,
			))
		case application.ImportFormatICS:
			events, err := componentCandidates(
				content,
				path,
				"VEVENT",
				format,
			)
			if err != nil {
				return nil, 0, err
			}
			result = append(result, events...)
		case application.ImportFormatVCF:
			contacts, err := componentCandidates(
				content,
				path,
				"VCARD",
				format,
			)
			if err != nil {
				return nil, 0, err
			}
			result = append(result, contacts...)
		case application.ImportFormatAuto,
			application.ImportFormatMixed,
			application.ImportFormatMBOX,
			application.ImportFormatMaildir,
			application.ImportFormatPST,
			application.ImportFormatOLM,
			application.ImportFormatThunderbird:
			return nil, 0, errors.New("invalid homogeneous import format")
		default:
			return nil, 0, errors.New("unknown homogeneous import format")
		}
		if len(result) > application.MaxImportPlanItems {
			return nil, 0, errors.New("import source contains too many items")
		}
	}
	return result, total, nil
}

func scanMixed(ctx context.Context, root string) (scanResult, error) {
	result := scanResult{format: application.ImportFormatMixed}
	paths, err := collectFiles(ctx, root, func(path string) bool {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".mbox", ".eml", ".ics", ".vcf", ".vcard", ".pst", ".olm":
			return true
		default:
			return false
		}
	})
	if err != nil {
		return scanResult{}, err
	}
	gated := make(map[application.ImportFormat]struct{})
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			return scanResult{}, fmt.Errorf("inspect mixed import source: %w", err)
		}
		format, err := detectFormat(
			path,
			info,
			application.ImportFormatAuto,
		)
		if err != nil {
			return scanResult{}, err
		}
		if format == application.ImportFormatPST ||
			format == application.ImportFormatOLM {
			if _, exists := gated[format]; !exists {
				result.gates = append(result.gates, archiveDecisionGate(format))
				gated[format] = struct{}{}
			}
			continue
		}
		if format == application.ImportFormatMBOX {
			items, bytesRead, err := scanMBOX(ctx, path)
			if err != nil {
				return scanResult{}, err
			}
			result.candidates = append(result.candidates, items...)
			result.bytesRead += bytesRead
			continue
		}
		content, err := readSourceFile(path, application.MaxImportItemBytes)
		if err != nil {
			return scanResult{}, err
		}
		result.bytesRead += int64(len(content))
		switch format {
		case application.ImportFormatEML:
			folder := filepath.ToSlash(
				filepath.Dir(safeRelative(root, path)),
			)
			if folder == "." {
				folder = filepath.Base(root)
			}
			result.candidates = append(
				result.candidates,
				mailImportCandidate(
					content,
					path,
					1,
					folder,
					nil,
					format,
					nil,
				),
			)
		case application.ImportFormatICS:
			items, err := componentCandidates(content, path, "VEVENT", format)
			if err != nil {
				return scanResult{}, err
			}
			result.candidates = append(result.candidates, items...)
		case application.ImportFormatVCF:
			items, err := componentCandidates(content, path, "VCARD", format)
			if err != nil {
				return scanResult{}, err
			}
			result.candidates = append(result.candidates, items...)
		case application.ImportFormatAuto,
			application.ImportFormatMixed,
			application.ImportFormatMBOX,
			application.ImportFormatMaildir,
			application.ImportFormatPST,
			application.ImportFormatOLM,
			application.ImportFormatThunderbird:
			return scanResult{}, errors.New("invalid mixed import member format")
		default:
			return scanResult{}, errors.New("unknown mixed import member format")
		}
		if len(result.candidates) > application.MaxImportPlanItems ||
			result.bytesRead > application.MaxImportSourceBytes {
			return scanResult{}, errors.New("mixed import source exceeds scan bounds")
		}
	}
	return result, nil
}

func scanMBOX(
	ctx context.Context,
	path string,
) ([]candidate, int64, error) {
	file, size, err := openSourceFile(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	if size > application.MaxImportSourceBytes {
		return nil, 0, errors.New("mbox source exceeds the total byte limit")
	}
	reader := bufio.NewReader(file)
	var current bytes.Buffer
	result := make([]candidate, 0, 128)
	foundDelimiter := false
	fromEscaping := false
	ordinal := 0
	finalize := func() error {
		if current.Len() == 0 {
			return nil
		}
		ordinal++
		degradations := []domain.Degradation(nil)
		if fromEscaping {
			degradations = append(degradations, domain.Degradation{
				Feature: "import.mbox_from_escaping",
				Reason:  "mboxrd From-line escaping was removed while reconstructing raw MIME",
				Lossy:   true,
			})
		}
		raw := append([]byte(nil), current.Bytes()...)
		result = append(result, mailImportCandidate(
			raw,
			path,
			ordinal,
			strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
			nil,
			application.ImportFormatMBOX,
			degradations,
		))
		current.Reset()
		fromEscaping = false
		if len(result) > application.MaxImportPlanItems {
			return errors.New("mbox source contains too many messages")
		}
		return nil
	}
	var bytesRead int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		line, readErr := reader.ReadBytes('\n')
		bytesRead += int64(len(line))
		if bytesRead > application.MaxImportSourceBytes {
			return nil, 0, errors.New("mbox source exceeds the total byte limit")
		}
		if isMBOXDelimiter(line) {
			if err := finalize(); err != nil {
				return nil, 0, err
			}
			foundDelimiter = true
		} else {
			if !foundDelimiter {
				if len(bytes.TrimSpace(line)) != 0 {
					return nil, 0, errors.New(
						"MBOX source does not begin with a valid From delimiter",
					)
				}
			} else {
				if bytes.HasPrefix(line, []byte(">From ")) {
					line = line[1:]
					fromEscaping = true
				}
				if current.Len()+len(line) > application.MaxImportItemBytes {
					return nil, 0, errors.New("mbox message exceeds the item byte limit")
				}
				_, _ = current.Write(line)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, 0, fmt.Errorf("read MBOX source: %w", readErr)
		}
	}
	if !foundDelimiter {
		return nil, 0, errors.New("mbox source contains no messages")
	}
	if err := finalize(); err != nil {
		return nil, 0, err
	}
	return result, bytesRead, nil
}

func isMBOXDelimiter(line []byte) bool {
	fields := strings.Fields(strings.TrimRight(string(line), "\r\n"))
	if len(fields) < 7 || fields[0] != "From" {
		return false
	}
	switch fields[2] {
	case "Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun":
		return true
	default:
		return false
	}
}

func scanMaildir(
	ctx context.Context,
	root string,
) ([]candidate, int64, []domain.Degradation, error) {
	if !isMaildir(root) {
		return nil, 0, nil, errors.New(
			"maildir source must contain cur and new directories",
		)
	}
	paths, err := collectFiles(ctx, root, func(path string) bool {
		parent := filepath.Base(filepath.Dir(path))
		return parent == "cur" || parent == "new"
	})
	if err != nil {
		return nil, 0, nil, err
	}
	result := make([]candidate, 0, len(paths))
	var total int64
	for index, path := range paths {
		content, err := readSourceFile(path, application.MaxImportItemBytes)
		if err != nil {
			return nil, 0, nil, err
		}
		total += int64(len(content))
		if total > application.MaxImportSourceBytes {
			return nil, 0, nil, errors.New("maildir source exceeds the total byte limit")
		}
		container := filepath.Dir(filepath.Dir(path))
		folder := safeRelative(root, container)
		if folder == filepath.Base(container) && container == root {
			folder = filepath.Base(root)
		}
		result = append(result, mailImportCandidate(
			content,
			path,
			index+1,
			filepath.ToSlash(folder),
			maildirFlags(filepath.Base(path)),
			application.ImportFormatMaildir,
			nil,
		))
	}
	return result, total, []domain.Degradation{{
		Feature: "import.maildir_labels",
		Reason:  "Maildir folders are retained as source folders; provider label mapping is deferred",
	}}, nil
}

func mailImportCandidate(
	raw []byte,
	path string,
	ordinal int,
	folder string,
	sourceFlags []string,
	format application.ImportFormat,
	degradations []domain.Degradation,
) candidate {
	object := contentDigest(raw)
	messageID := ""
	originalDate := ""
	flags := append([]string(nil), sourceFlags...)
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		degradations = append(degradations, domain.Degradation{
			Feature: "import.mime_headers",
			Reason:  "message headers could not be parsed; raw MIME was retained",
		})
	} else {
		rawMessageID := message.Header.Get("Message-ID")
		messageID = cleanHeaderIdentity(rawMessageID)
		if rawMessageID != "" && messageID == "" {
			degradations = append(degradations, domain.Degradation{
				Feature: "import.message_id",
				Reason:  "message Message-ID header was malformed; raw MIME retains the original value",
			})
		}
		if rawDate := message.Header.Get("Date"); rawDate != "" {
			if parsed, parseErr := mail.ParseDate(rawDate); parseErr == nil {
				originalDate = parsed.UTC().Format(time.RFC3339)
			} else {
				degradations = append(degradations, domain.Degradation{
					Feature: "import.original_date",
					Reason:  "message Date header was malformed; raw MIME retains the original value",
				})
			}
		}
		flags = append(flags, mboxHeaderFlags(
			message.Header.Get("Status"),
			message.Header.Get("X-Status"),
		)...)
	}
	slices.Sort(flags)
	flags = slices.Compact(flags)
	identity := ""
	if messageID != "" {
		identity = "mail:" + strings.ToLower(messageID)
	}
	exact := dedupeDigest(
		"mail",
		identity,
		originalDate,
		strings.Join(flags, ","),
		folder,
		object,
	)
	return candidate{
		identity: identity,
		raw:      raw,
		item: application.ImportItem{
			Kind: "mail", ObjectSHA256: object, DedupeKey: exact,
			MessageID: messageID, OriginalDate: originalDate,
			Flags: flags, Folder: folder,
			Source: application.ImportSourceProvenance{
				Path: cleanSourcePath(path), Format: format, Ordinal: ordinal,
			},
			Degradations: degradations,
		},
	}
}

func cleanHeaderIdentity(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 998 || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func mboxHeaderFlags(status, extended string) []string {
	result := make([]string, 0, 5)
	if strings.Contains(status, "R") {
		result = append(result, "seen")
	}
	if strings.Contains(extended, "A") {
		result = append(result, "answered")
	}
	if strings.Contains(extended, "F") {
		result = append(result, "flagged")
	}
	if strings.Contains(extended, "T") {
		result = append(result, "trashed")
	}
	if strings.Contains(extended, "D") {
		result = append(result, "draft")
	}
	return result
}

func maildirFlags(name string) []string {
	_, encoded, found := strings.Cut(name, ":2,")
	if !found {
		return nil
	}
	flags := make([]string, 0, 6)
	for _, flag := range encoded {
		switch flag {
		case 'S':
			flags = append(flags, "seen")
		case 'R':
			flags = append(flags, "answered")
		case 'F':
			flags = append(flags, "flagged")
		case 'T':
			flags = append(flags, "trashed")
		case 'D':
			flags = append(flags, "draft")
		case 'P':
			flags = append(flags, "passed")
		}
	}
	return flags
}

func componentCandidates(
	raw []byte,
	path, component string,
	format application.ImportFormat,
) ([]candidate, error) {
	components, err := extractComponents(raw, component)
	if err != nil {
		return nil, fmt.Errorf("scan %s source: %w", format, err)
	}
	result := make([]candidate, 0, len(components))
	for index, content := range components {
		properties := componentProperties(content)
		object := contentDigest(content)
		item := application.ImportItem{
			ObjectSHA256: object,
			Source: application.ImportSourceProvenance{
				Path: cleanSourcePath(path), Format: format, Ordinal: index + 1,
			},
		}
		identity := ""
		switch component {
		case "VEVENT":
			item.Kind = "event"
			item.CalendarUID = cleanComponentIdentity(properties["UID"])
			item.RecurrenceID = cleanComponentIdentity(properties["RECURRENCE-ID"])
			if item.CalendarUID != "" {
				identity = "event:" + strings.ToLower(item.CalendarUID) +
					":" + item.RecurrenceID
			} else {
				item.Degradations = append(item.Degradations, domain.Degradation{
					Feature: "import.calendar_uid",
					Reason:  "VEVENT has no usable UID; content hash deduplication is used",
				})
			}
		case "VCARD":
			item.Kind = "contact"
			item.ContactUID = cleanComponentIdentity(properties["UID"])
			if item.ContactUID != "" {
				identity = "contact:" + strings.ToLower(item.ContactUID)
			} else {
				item.Degradations = append(item.Degradations, domain.Degradation{
					Feature: "import.contact_uid",
					Reason:  "VCARD has no usable UID; content hash deduplication is used",
				})
			}
		}
		item.DedupeKey = dedupeDigest(item.Kind, identity, object)
		result = append(result, candidate{
			item: item, raw: content, identity: identity,
		})
	}
	return result, nil
}

func extractComponents(raw []byte, name string) ([][]byte, error) {
	lines := bytes.SplitAfter(raw, []byte{'\n'})
	begin := "BEGIN:" + name
	end := "END:" + name
	result := make([][]byte, 0, 16)
	var current bytes.Buffer
	inComponent := false
	depth := 0
	for _, line := range lines {
		value := strings.ToUpper(strings.TrimSpace(string(line)))
		if !inComponent {
			if value == begin {
				inComponent = true
				depth = 1
				current.Reset()
				_, _ = current.Write(line)
			}
			continue
		}
		if current.Len()+len(line) > application.MaxImportItemBytes {
			return nil, errors.New("component exceeds the item byte limit")
		}
		_, _ = current.Write(line)
		if strings.HasPrefix(value, "BEGIN:") {
			depth++
		}
		if strings.HasPrefix(value, "END:") {
			depth--
			if depth == 0 {
				if value != end {
					return nil, errors.New("component nesting is malformed")
				}
				result = append(result, append([]byte(nil), current.Bytes()...))
				inComponent = false
				if len(result) > application.MaxImportPlanItems {
					return nil, errors.New("source contains too many components")
				}
			}
		}
	}
	if inComponent {
		return nil, errors.New("component is not terminated")
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("source contains no %s component", name)
	}
	return result, nil
}

func componentProperties(raw []byte) map[string]string {
	physical := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	logical := make([]string, 0, len(physical))
	for _, line := range physical {
		if len(line) != 0 && (line[0] == ' ' || line[0] == '\t') &&
			len(logical) != 0 {
			logical[len(logical)-1] += line[1:]
		} else {
			logical = append(logical, line)
		}
	}
	result := make(map[string]string)
	for _, line := range logical {
		head, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name, _, _ := strings.Cut(head, ";")
		name = strings.ToUpper(name)
		if _, exists := result[name]; !exists {
			result[name] = value
		}
	}
	return result
}

func cleanComponentIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 1024 || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func archiveDecisionGate(format application.ImportFormat) application.ImportDecisionGate {
	return application.ImportDecisionGate{
		Format: format,
		Reason: "no parser is selected until licensing, fidelity, and untrusted-input security are reviewed",
		Gates: []string{
			"license compatibility",
			"raw data and metadata fidelity",
			"memory-safe bounded parsing",
		},
	}
}

func collectFiles(
	ctx context.Context,
	root string,
	include func(string) bool,
) ([]string, error) {
	result := make([]string, 0, 128)
	entries := 0
	err := filepath.WalkDir(root, func(
		path string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > maximumWalkEntries {
			return errors.New("import directory contains too many entries")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New(
				"import directory contains a symbolic link; select a source without linked entries",
			)
		}
		if len(path) > 4096 || strings.ContainsAny(path, "\r\n\x00") {
			return errors.New("import directory contains a malformed source path")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New(
				"import directory contains a non-regular entry; select a source with regular files only",
			)
		}
		if include(path) {
			result = append(result, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk import source: %w", err)
	}
	slices.Sort(result)
	return result, nil
}

func readSourceFile(path string, maximum int) ([]byte, error) {
	file, size, err := openSourceFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	if size > int64(maximum) {
		return nil, errors.New("import item exceeds the configured byte limit")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, fmt.Errorf("read import source: %w", err)
	}
	if len(content) > maximum {
		return nil, errors.New("import item exceeds the configured byte limit")
	}
	return content, nil
}

func openSourceFile(path string) (*os.File, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("inspect import source file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, errors.New("import source item is not a regular file")
	}
	// #nosec G304 -- the user explicitly approved this source path.
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open import source file: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, 0, errors.New("import source changed while it was opened")
	}
	return file, opened.Size(), nil
}

func isMaildir(path string) bool {
	for _, name := range []string{"cur", "new"} {
		info, err := os.Lstat(filepath.Join(path, name))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func sortCandidates(values []candidate) {
	slices.SortStableFunc(values, func(left, right candidate) int {
		if compared := strings.Compare(
			left.item.Source.Path,
			right.item.Source.Path,
		); compared != 0 {
			return compared
		}
		return left.item.Source.Ordinal - right.item.Source.Ordinal
	})
}
