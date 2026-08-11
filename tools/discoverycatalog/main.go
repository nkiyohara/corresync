// Command discoverycatalog keeps the public DNS signal artifact synchronized
// with the CLI's canonical credential-free discovery knowledge.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/nkiyohara/corresync/internal/discovery"
	"github.com/nkiyohara/corresync/internal/rollout"
)

const defaultPath = "web/discovery-worker/catalog.json"

type artifact struct {
	SchemaVersion       int                      `json:"schemaVersion"`
	GoogleOAuthApproved bool                     `json:"googleOAuthApproved"`
	Families            []discovery.SignalFamily `json:"families"`
}

func main() {
	check := flag.Bool("check", false, "fail unless the checked-in artifact is current")
	path := flag.String("path", defaultPath, "artifact path")
	flag.Parse()

	content, err := render()
	if err == nil && *check {
		err = compare(*path, content)
	} else if err == nil {
		err = os.WriteFile(*path, content, 0o644) // #nosec G306 -- public generated data.
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "discovery catalog:", err)
		os.Exit(1)
	}
}

func render() ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(artifact{
		SchemaVersion:       1,
		GoogleOAuthApproved: rollout.GoogleOAuthApproved,
		Families:            discovery.SignalCatalog(),
	}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func compare(path string, expected []byte) error {
	actual, err := os.ReadFile(path) // #nosec G304 -- repository-owned path.
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("checked-in artifact is stale; run task discovery:generate")
	}
	return nil
}
