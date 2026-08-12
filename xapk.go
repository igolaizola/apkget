package apkget

import (
	"archive/zip"
	"fmt"
	"path/filepath"
	"strings"
)

func validateXAPK(bundlePath string) error {
	archive, err := zip.OpenReader(bundlePath)
	if err != nil {
		return fmt.Errorf("not a ZIP/XAPK archive: %w", err)
	}
	defer func() { _ = archive.Close() }()

	for _, file := range archive.File {
		name := strings.ToLower(filepath.ToSlash(file.Name))
		if !file.FileInfo().IsDir() && strings.HasSuffix(name, ".apk") {
			return nil
		}
	}
	return fmt.Errorf("archive contains no nested APK files")
}
