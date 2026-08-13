package integrationlifecycle

import (
	"os"
	"path/filepath"
)

func testAbsolutePath(elements ...string) string {
	parts := append([]string{os.TempDir(), "corresync-lifecycle-tests"}, elements...)
	return filepath.Join(parts...)
}
