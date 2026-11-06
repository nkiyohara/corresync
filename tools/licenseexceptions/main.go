// Command licenseexceptions verifies and packages reviewed license text that
// the pinned go-licenses classifier cannot recognize.
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// segmentio/asm v1.2.1 uses MIT-0 (MIT No Attribution). Review upstream's
// exact LICENSE again if this digest changes; never silently skip a new text.
const asmLicenseDigest = "cca993712df289a5958bdef69031a5dac0f951ac15afeb313f9eeea55ed59443"

func main() {
	save := flag.Bool("save", false, "include the checked license in the release bundle")
	flag.Parse()
	if err := run(*save); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(save bool) error {
	if flag.NArg() != 0 {
		return errors.New("licenseexceptions does not accept positional arguments")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	directory, err := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-m", "-f={{.Dir}}", "github.com/segmentio/asm").Output()
	if err != nil {
		return fmt.Errorf("resolve pinned asm module: %w", err)
	}
	path := filepath.Join(strings.TrimSpace(string(directory)), "LICENSE")
	file, err := os.Open(path) // #nosec G304,G703 -- exact module-cache LICENSE resolved by the local Go tool.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil {
		return err
	}
	if err := checkLicense(data); err != nil {
		return err
	}
	if save {
		const destination = ".release/third_party_licenses/github.com/segmentio/asm/LICENSE"
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil { // #nosec G301 -- public license material.
			return err
		}
		return os.WriteFile(destination, data, 0o644) // #nosec G306 -- public license material.
	}
	return nil
}

func checkLicense(data []byte) error {
	if fmt.Sprintf("%x", sha256.Sum256(data)) != asmLicenseDigest {
		return errors.New("segmentio/asm LICENSE changed; review its terms before updating the pinned digest")
	}
	return nil
}
