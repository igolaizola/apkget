package apkget

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeSource struct {
	name string
	err  error
}

type fakeVersionSource struct {
	fakeSource
	versions []VersionInfo
}

func (s fakeVersionSource) ListVersions(context.Context, string) ([]VersionInfo, error) {
	return s.versions, s.err
}

func (s fakeSource) Name() string                                      { return s.name }
func (s fakeSource) Search(context.Context, string) ([]AppInfo, error) { return nil, s.err }
func (s fakeSource) Info(context.Context, string) (*AppInfo, error)    { return nil, s.err }
func (s fakeSource) Download(_ context.Context, packageName, dir, version string) (DownloadResult, error) {
	if s.err != nil {
		return DownloadResult{}, s.err
	}
	path := filepath.Join(dir, packageName+"-"+version+".apk")
	if err := os.WriteFile(path, []byte("apk"), 0o644); err != nil {
		return DownloadResult{}, err
	}
	return sourceResult(s.name, packageName, version, path, 3, "hash"), nil
}

func TestDownloaderFallsBackAndUsesOutputDirectory(t *testing.T) {
	dir := t.TempDir()
	d := NewDownloader(nil, []Source{
		fakeSource{name: "first", err: os.ErrNotExist},
		fakeSource{name: "second"},
	})
	result, err := d.Download(context.Background(), "com.example.demo", Options{OutputDir: dir, Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "second" || result.Path != filepath.Join(dir, "com.example.demo-1.apk") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestDownloaderListsVersionsWithoutDownloading(t *testing.T) {
	source := fakeVersionSource{
		fakeSource: fakeSource{name: "test"},
		versions: []VersionInfo{
			{Versions: []string{"2.0"}},
			{Source: "test", Versions: []string{"1.0", "2.0"}},
		},
	}
	d := NewDownloader(nil, []Source{source})
	versions, err := d.ListVersions(context.Background(), "com.example.demo", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Package != "com.example.demo" || versions[0].Source != "test" || len(versions[0].Versions) != 2 {
		t.Fatalf("unexpected versions: %+v", versions)
	}
}
