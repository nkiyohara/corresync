package importstage

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/nkiyohara/corresync/internal/application"
	"github.com/nkiyohara/corresync/internal/domain"
)

const (
	maximumProfilesINIBytes = 1 << 20
	maximumPrefsJSBytes     = 4 << 20
)

type thunderbirdProfile struct {
	Path       string
	IsRelative bool
}

type thunderbirdServer struct {
	Type     string
	Host     string
	Identity string
}

func scanThunderbird(
	ctx context.Context,
	source string,
	info os.FileInfo,
) (scanResult, error) {
	root := source
	profilesPath := filepath.Join(source, "profiles.ini")
	if info.Mode().IsRegular() {
		if !strings.EqualFold(filepath.Base(source), "profiles.ini") {
			return scanResult{}, errors.New(
				"thunderbird file source must be profiles.ini",
			)
		}
		root = filepath.Dir(source)
		profilesPath = source
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return scanResult{}, fmt.Errorf("open Thunderbird source root: %w", err)
	}
	defer func() { _ = rootHandle.Close() }()
	profilesRelative := filepath.Base(profilesPath)
	encoded, err := readRootSourceFile(
		rootHandle,
		profilesRelative,
		maximumProfilesINIBytes,
	)
	if err != nil {
		return scanResult{}, err
	}
	profiles, err := parseThunderbirdProfiles(encoded)
	if err != nil {
		return scanResult{}, err
	}
	result := scanResult{
		format:    application.ImportFormatThunderbird,
		bytesRead: int64(len(encoded)),
		degradations: []domain.Degradation{
			{
				Feature: "import.desktop_credentials",
				Reason:  "Thunderbird credential databases, OAuth state, cookies, and session material were not read or copied",
			},
			{
				Feature: "import.imap_cache",
				Reason:  "Thunderbird IMAP caches are not archives and were not staged; reconnect the account instead",
			},
		},
	}
	for _, profile := range profiles {
		if err := ctx.Err(); err != nil {
			return scanResult{}, err
		}
		if !profile.IsRelative || filepath.IsAbs(profile.Path) {
			result.degradations = append(result.degradations, domain.Degradation{
				Feature: "import.desktop_profile_path",
				Reason:  "an absolute Thunderbird profile path was not followed outside the explicitly approved source root",
			})
			continue
		}
		profilePath := filepath.Clean(filepath.FromSlash(profile.Path))
		if !pathWithin(".", profilePath) {
			return scanResult{}, errors.New(
				"thunderbird profile path escapes the approved source root",
			)
		}
		profileInfo, err := rootHandle.Lstat(profilePath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return scanResult{}, fmt.Errorf("inspect Thunderbird profile: %w", err)
		}
		if !profileInfo.IsDir() || profileInfo.Mode()&os.ModeSymlink != 0 {
			return scanResult{}, errors.New(
				"thunderbird profile is not a regular directory",
			)
		}
		prefsPath := filepath.Join(profilePath, "prefs.js")
		prefs, err := readRootSourceFile(
			rootHandle,
			prefsPath,
			maximumPrefsJSBytes,
		)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return scanResult{}, err
		}
		result.bytesRead += int64(len(prefs))
		if result.bytesRead > application.MaxImportSourceBytes {
			return scanResult{}, errors.New(
				"thunderbird configuration exceeds the scan byte limit",
			)
		}
		servers, err := parseThunderbirdServers(prefs)
		if err != nil {
			return scanResult{}, err
		}
		for _, server := range servers {
			result.hints = append(result.hints, application.ImportDesktopHint{
				Application: "thunderbird",
				AccountType: server.Type,
				Host:        server.Host,
				Identity:    server.Identity,
			})
		}
		if len(result.hints) > application.MaxImportDesktopHints {
			return scanResult{}, errors.New(
				"thunderbird configuration contains too many account hints",
			)
		}
	}
	slices.SortFunc(result.hints, func(
		left, right application.ImportDesktopHint,
	) int {
		if compared := strings.Compare(left.AccountType, right.AccountType); compared != 0 {
			return compared
		}
		if compared := strings.Compare(left.Host, right.Host); compared != 0 {
			return compared
		}
		return strings.Compare(left.Identity, right.Identity)
	})
	result.hints = slices.Compact(result.hints)
	return result, nil
}

func parseThunderbirdProfiles(data []byte) ([]thunderbirdProfile, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	result := make([]thunderbirdProfile, 0, 8)
	current := -1
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if strings.HasPrefix(strings.ToLower(line), "[profile") {
				result = append(result, thunderbirdProfile{})
				current = len(result) - 1
			} else {
				current = -1
			}
			continue
		}
		if current < 0 || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "path":
			value = strings.TrimSpace(value)
			if value == "" || len(value) > 2048 ||
				strings.ContainsAny(value, "\r\n\x00") {
				return nil, errors.New("thunderbird profile path is malformed")
			}
			result[current].Path = value
		case "isrelative":
			result[current].IsRelative = strings.TrimSpace(value) == "1"
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan Thunderbird profiles.ini: %w", err)
	}
	filtered := result[:0]
	for _, profile := range result {
		if profile.Path != "" {
			filtered = append(filtered, profile)
		}
	}
	return filtered, nil
}

func parseThunderbirdServers(data []byte) ([]thunderbirdServer, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	servers := make(map[string]*thunderbirdServer)
	const prefix = `user_pref("mail.server.`
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(line, prefix)
		nameEnd := strings.Index(remainder, `"`)
		if nameEnd < 0 {
			continue
		}
		key := remainder[:nameEnd]
		parts := strings.Split(key, ".")
		if len(parts) != 2 {
			continue
		}
		switch parts[1] {
		case "type", "hostname", "userName":
		default:
			continue
		}
		comma := strings.Index(line, ",")
		end := strings.LastIndex(line, ");")
		if comma < 0 || end <= comma {
			continue
		}
		rawValue := strings.TrimSpace(line[comma+1 : end])
		value, err := strconv.Unquote(rawValue)
		if err != nil || len(value) > 1024 ||
			strings.ContainsAny(value, "\r\n\x00") {
			continue
		}
		server := servers[parts[0]]
		if server == nil {
			server = &thunderbirdServer{}
			servers[parts[0]] = server
		}
		switch parts[1] {
		case "type":
			switch strings.ToLower(value) {
			case "imap", "pop3", "nntp", "none":
				server.Type = strings.ToLower(value)
			}
		case "hostname":
			if len(value) <= 253 &&
				!strings.ContainsAny(value, " /@") {
				server.Host = strings.ToLower(value)
			}
		case "userName":
			if len(value) <= 320 {
				server.Identity = value
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan Thunderbird prefs.js: %w", err)
	}
	keys := make([]string, 0, len(servers))
	for key := range servers {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]thunderbirdServer, 0, len(keys))
	for _, key := range keys {
		server := *servers[key]
		if server.Type != "" || server.Host != "" || server.Identity != "" {
			result = append(result, server)
		}
	}
	return result, nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil &&
		relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readRootSourceFile(
	root *os.Root,
	name string,
	maximum int,
) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("import source item is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open import source file: %w", err)
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("import source changed while it was opened")
	}
	if opened.Size() > int64(maximum) {
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
