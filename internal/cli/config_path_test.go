package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveConfigPathPrefersDotfile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, dir, defaultConfigFile, `{}`)
	writeConfig(t, dir, legacyConfigFile, `{}`)

	got, err := resolveConfigPath(defaultConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != defaultConfigFile {
		t.Fatalf("path = %q, want %q", got, defaultConfigFile)
	}
}

func TestResolveConfigPathUsesLegacy(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, dir, legacyConfigFile, `{}`)

	got, err := resolveConfigPath(defaultConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != legacyConfigFile {
		t.Fatalf("path = %q, want %q", got, legacyConfigFile)
	}
}

func TestResolveConfigPathExplicitLegacy(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeConfig(t, dir, legacyConfigFile, `{}`)

	got, err := resolveConfigPath(legacyConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != legacyConfigFile {
		t.Fatalf("path = %q", got)
	}
}

func writeConfig(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
