package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOutputTarget(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "apks")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		file bool
	}{
		{name: "existing directory", path: dir, file: false},
		{name: "new directory", path: filepath.Join(root, "new-dir"), file: false},
		{name: "directory with trailing slash", path: filepath.Join(root, "new-dir-2") + string(os.PathSeparator), file: false},
		{name: "new file", path: filepath.Join(root, "idealista.apk"), file: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, file, err := outputTarget(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if path != test.path {
				t.Fatalf("path = %q, want %q", path, test.path)
			}
			if file != test.file {
				t.Fatalf("file = %v, want %v", file, test.file)
			}
		})
	}
}

func TestMoveFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.xapk")
	target := filepath.Join(root, "nested", "idealista.apk")
	contents := []byte("test apk contents")
	if err := os.WriteFile(source, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := moveFile(source, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(contents) {
		t.Fatalf("target contents = %q, want %q", got, contents)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists or returned unexpected error: %v", err)
	}
}

func TestPrepareReverseInputKeepsLocalFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.apk")
	if err := os.WriteFile(path, []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, cleanup, err := prepareReverseInput(context.Background(), path, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if got != path {
		t.Fatalf("input path = %q, want %q", got, path)
	}
}

func TestIsAPKInputPath(t *testing.T) {
	for _, extension := range []string{".apk", ".apkx", ".xapk", ".apks", ".apkm", ".APK"} {
		if !isAPKInputPath("app" + extension) {
			t.Errorf("isAPKInputPath rejected %s", extension)
		}
	}
	if isAPKInputPath("telegram") {
		t.Error("isAPKInputPath accepted an app name")
	}
}

func TestReverseRejectsConflictingOutputArguments(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "sample.apk")
	if err := os.WriteFile(input, []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reverse(context.Background(), []string{"-output", filepath.Join(root, "one"), input, filepath.Join(root, "two")}); err == nil {
		t.Fatal("reverse accepted both -output and positional output_dir")
	}
}
