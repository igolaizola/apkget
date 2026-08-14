package apkget

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectBundleAPKUsesManifestBase(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "sample.xapk")
	writeTestBundle(t, archivePath, map[string][]byte{
		"manifest.json":    []byte(`{"split_apks":[{"id":"base","file":"payload/main.apk"}]}`),
		"payload/main.apk": []byte("base APK"),
		"config/large.apk": []byte(strings.Repeat("config", 20)),
	})

	root := t.TempDir()
	selected, err := selectBundleAPK(archivePath, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "payload", "main.apk"); selected != want {
		t.Fatalf("selected APK = %q, want %q", selected, want)
	}
}

func TestSelectBundleAPKUsesInfoBaseField(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "sample.apkm")
	writeTestBundle(t, archivePath, map[string][]byte{
		"info.json":     []byte(`{"base":{"filename":"base-main.apk"}}`),
		"base-main.apk": []byte("base APK"),
		"other.apk":     []byte(strings.Repeat("other", 20)),
	})

	root := t.TempDir()
	selected, err := selectBundleAPK(archivePath, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "base-main.apk"); selected != want {
		t.Fatalf("selected APK = %q, want %q", selected, want)
	}
}

func TestSelectBundleAPKFallsBackToLargestNonConfigAPK(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "sample.apks")
	writeTestBundle(t, archivePath, map[string][]byte{
		"config.x86.apk": []byte(strings.Repeat("config", 100)),
		"small.apk":      []byte("small"),
		"main.apk":       []byte(strings.Repeat("main", 20)),
	})

	root := t.TempDir()
	selected, err := selectBundleAPK(archivePath, root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "main.apk"); selected != want {
		t.Fatalf("selected APK = %q, want %q", selected, want)
	}
}

func TestExtractZIPRejectsPathTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "unsafe.xapk")
	writeTestBundle(t, archivePath, map[string][]byte{"../outside.apk": []byte("unsafe")})

	err := extractZIP(archivePath, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("extractZIP error = %v, want unsafe path error", err)
	}
}

func TestIsReverseInput(t *testing.T) {
	for _, extension := range []string{".apk", ".apkx", ".xapk", ".apks", ".apkm", ".APK"} {
		if !isReverseInput("app" + extension) {
			t.Errorf("isReverseInput rejected %s", extension)
		}
	}
	if isReverseInput("app.zip") {
		t.Error("isReverseInput accepted .zip")
	}
}

func TestVerifySHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool")
	content := []byte("pinned tool content")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	wantBytes := sha256.Sum256(content)
	want := hex.EncodeToString(wantBytes[:])
	if err := verifySHA256(path, want); err != nil {
		t.Fatal(err)
	}
	if err := verifySHA256(path, strings.Repeat("0", sha256.Size*2)); err == nil {
		t.Fatal("verifySHA256 accepted an incorrect digest")
	}
}

func TestToolSHA256(t *testing.T) {
	for _, rawURL := range []string{apktoolURL, jadxURL} {
		if digest, ok := toolSHA256(rawURL); !ok || len(digest) != sha256.Size*2 {
			t.Fatalf("toolSHA256(%q) = %q, %v", rawURL, digest, ok)
		}
	}
	if _, ok := toolSHA256("https://example.com/tool"); ok {
		t.Fatal("toolSHA256 accepted an unpinned URL")
	}
}

func writeTestBundle(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for name, content := range files {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
