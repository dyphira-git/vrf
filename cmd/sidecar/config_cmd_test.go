package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creachadair/tomledit/parser"
)

func TestConfigSet_UpdatesValueAndPreservesComments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "vrf.toml")

	initial := strings.TrimSpace(`
	# header comment
	http = "http://127.0.0.1:8081"
	data_dir = "`+filepath.ToSlash(filepath.Join(dir, "drand"))+`"
	no_restart = false
	restart_backoff_min = "1s"
`) + "\n"

	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial vrf.toml: %v", err)
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"--file", path, "set", "no_restart", "true"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected Execute() success, got: %v", err)
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated vrf.toml: %v", err)
	}
	if !strings.Contains(string(updated), "# header comment") {
		t.Fatalf("expected header comment to be preserved, got:\n%s", string(updated))
	}

	doc, _, err := loadTomlDocument(path)
	if err != nil {
		t.Fatalf("loadTomlDocument: %v", err)
	}
	ent, err := findUniqueTomlMapping(doc, parser.Key{"no_restart"})
	if err != nil {
		t.Fatalf("findUniqueTomlMapping(no_restart): %v", err)
	}
	if got := ent.Value.String(); got != "true" {
		t.Fatalf("expected no_restart=true, got %q", got)
	}
}

func TestConfigSet_MissingKeyFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "vrf.toml")

	initial := `data_dir = "` + filepath.ToSlash(filepath.Join(dir, "drand")) + `"` + "\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial vrf.toml: %v", err)
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"--file", path, "set", "does_not_exist", "123"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected Execute() to fail for missing key")
	}
}

func TestConfigSet_InvalidRestartBackoffFailsValidation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "vrf.toml")

	initial := strings.TrimSpace(`
	http = "http://127.0.0.1:8081"
	data_dir = "`+filepath.ToSlash(filepath.Join(dir, "drand"))+`"
	restart_backoff_min = "1s"
	`) + "\n"

	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial vrf.toml: %v", err)
	}

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"--file", path, "set", "restart_backoff_min", "nope"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected Execute() to fail for invalid restart_backoff_min")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vrf.toml: %v", err)
	}
	if string(after) != initial {
		t.Fatalf("expected file to remain unchanged on validation error, got:\n%s", string(after))
	}
}
