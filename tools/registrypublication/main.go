// Command registrypublication verifies stable release assets, post-verifies
// the production MCP Registry record, and emits content-free evidence.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/nkiyohara/corresync/internal/releasepublication"
)

func main() {
	assets := flag.String("assets", "assets", "directory containing downloaded release assets")
	tag := flag.String("tag", "", "stable release tag (vX.Y.Z)")
	commit := flag.String("commit", "", "exact 40-character source commit for evidence")
	evidencePath := flag.String("evidence", "", "write post-publication evidence to this path")
	checkExisting := flag.Bool("check-existing", false, "print present or absent for the exact immutable registry version")
	timeout := flag.Duration("timeout", 2*time.Minute, "maximum post-publication verification time")
	flag.Parse()

	if err := run(*assets, *tag, *commit, *evidencePath, *checkExisting, *timeout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "registry publication verification failed:", err)
		os.Exit(1)
	}
}

func run(assets, tag, commit, evidencePath string, checkExisting bool, timeout time.Duration) error {
	candidate, err := releasepublication.VerifyAssets(assets, tag)
	if err != nil {
		return err
	}
	if checkExisting && evidencePath != "" {
		return errors.New("--check-existing cannot be combined with --evidence")
	}
	if checkExisting {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		existing, err := releasepublication.CheckExisting(ctx, registryClient(), candidate)
		if err != nil {
			return err
		}
		if existing {
			fmt.Println("present")
		} else {
			fmt.Println("absent")
		}
		return nil
	}
	if evidencePath == "" {
		fmt.Printf("verified stable registry inputs for %s (%s)\n", candidate.Tag, candidate.PackageSHA256)
		return nil
	}
	if timeout < 10*time.Second || timeout > 5*time.Minute {
		return errors.New("registry verification timeout must be between 10s and 5m")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	record, err := waitForRegistry(ctx, registryClient(), candidate, 5*time.Second)
	if err != nil {
		return err
	}
	evidence, err := releasepublication.NewEvidence(candidate, record, commit, time.Now())
	if err != nil {
		return err
	}
	if err := releasepublication.WriteEvidence(evidencePath, evidence); err != nil {
		return err
	}
	fmt.Printf("verified active latest registry record for %s\n", candidate.Tag)
	return nil
}

func registryClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("registry redirects are disabled")
		},
	}
}

func waitForRegistry(
	ctx context.Context,
	client releasepublication.HTTPDoer,
	candidate releasepublication.Candidate,
	retryInterval time.Duration,
) (releasepublication.RegistryRecord, error) {
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()
	var lastErr error
	for {
		record, err := releasepublication.VerifyRegistry(ctx, client, candidate)
		if err == nil {
			return record, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return releasepublication.RegistryRecord{}, fmt.Errorf("production registry did not converge: %w", lastErr)
		case <-ticker.C:
		}
	}
}
