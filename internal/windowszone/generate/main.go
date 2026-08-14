package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"go/format"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	sourceURL = "https://raw.githubusercontent.com/unicode-org/cldr/" +
		"11299982335beb974c1c63c45265184e759c0f41/common/supplemental/windowsZones.xml"
	sourceSHA256  = "9cf3db6a31fb382fee21b70be6feba1e82766b0fcd06e6261fb7936a73e537ff"
	maxSourceSize = 1 << 20
)

type supplementalData struct {
	Zones []mapZone `xml:"windowsZones>mapTimezones>mapZone"`
}

type mapZone struct {
	WindowsID string `xml:"other,attr"`
	Territory string `xml:"territory,attr"`
	IANA      string `xml:"type,attr"`
}

func main() {
	if err := generate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate() error {
	client := &http.Client{Timeout: 30 * time.Second}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, sourceURL, nil)
	if err != nil {
		return fmt.Errorf("prepare CLDR Windows-zone download: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download CLDR Windows zones: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download CLDR Windows zones: HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxSourceSize+1))
	if err != nil {
		return fmt.Errorf("read CLDR Windows zones: %w", err)
	}
	if len(raw) > maxSourceSize {
		return errors.New("CLDR Windows-zone source exceeds the size bound")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != sourceSHA256 {
		return errors.New("CLDR Windows-zone source digest does not match")
	}

	var source supplementalData
	if err := xml.Unmarshal(raw, &source); err != nil {
		return fmt.Errorf("parse CLDR Windows zones: %w", err)
	}
	mappings := make(map[string]string)
	for _, zone := range source.Zones {
		if zone.Territory != "001" {
			continue
		}
		iana := strings.Fields(zone.IANA)
		if zone.WindowsID == "" || len(iana) != 1 {
			return errors.New("CLDR territory-neutral Windows-zone mapping is malformed")
		}
		if _, exists := mappings[zone.WindowsID]; exists {
			return errors.New("CLDR duplicates a territory-neutral Windows-zone mapping")
		}
		mappings[zone.WindowsID] = iana[0]
	}
	if len(mappings) < 100 {
		return errors.New("CLDR returned too few territory-neutral Windows-zone mappings")
	}

	keys := make([]string, 0, len(mappings))
	for key := range mappings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output bytes.Buffer
	fmt.Fprintln(&output, "// Code generated from Unicode CLDR release 48.2; DO NOT EDIT.")
	fmt.Fprintln(&output, "// Source commit: 11299982335beb974c1c63c45265184e759c0f41")
	fmt.Fprintln(&output, "package windowszone")
	fmt.Fprintln(&output, "\nvar windowsToIANA = map[string]string{")
	for _, key := range keys {
		fmt.Fprintf(&output, "\t%q: %q,\n", key, mappings[key])
	}
	fmt.Fprintln(&output, "}")
	formatted, err := format.Source(output.Bytes())
	if err != nil {
		return fmt.Errorf("format generated Windows zones: %w", err)
	}
	if err := os.WriteFile("zones_gen.go", formatted, 0o644); err != nil { //nolint:gosec // Generated Go source is repository-readable.
		return fmt.Errorf("write generated Windows zones: %w", err)
	}
	return nil
}
