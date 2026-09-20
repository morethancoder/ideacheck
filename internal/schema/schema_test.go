package schema

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// The published schema is what frontend authors build against; it must always
// match the Go types. Fix a failure with `make schema`.
func TestPublishedSchemaMatchesGoTypes(t *testing.T) {
	want, err := CheckResult()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale; run `make schema`", Path)
	}
}
