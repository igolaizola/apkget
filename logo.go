package apkget

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxLogoSize = 32 << 20

// DownloadLogo resolves an app name or package ID through Google Play and
// saves the app's Open Graph icon without downloading the application.
// destination may be a directory or a file path. Directories use the default
// name logo-<package-id>.<extension>.
func DownloadLogo(ctx context.Context, query, destination string, client *http.Client) (string, error) {
	client = defaultClient(client)
	packageID, err := ResolvePackageID(ctx, query, client)
	if err != nil {
		return "", err
	}
	pageURL := "https://play.google.com/store/apps/details?id=" + packageID + "&hl=en&gl=us"
	page, _, err := getText(ctx, client, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("fetch Google Play page: %w", err)
	}
	iconURL := playStoreIconURL(page)
	if iconURL == "" {
		return "", fmt.Errorf("no app logo found for %s", packageID)
	}

	req, err := newRequest(ctx, http.MethodGet, iconURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch app logo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch app logo: HTTP %s", resp.Status)
	}
	extension := logoExtension(resp.Header.Get("Content-Type"))
	if extension == "" {
		return "", fmt.Errorf("logo URL returned unsupported content type %q", resp.Header.Get("Content-Type"))
	}

	target, targetDir, err := logoTarget(destination, packageID, extension)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", fmt.Errorf("create logo directory: %w", err)
	}
	temporary, err := os.CreateTemp(targetDir, ".apkget-logo-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()

	// Read one byte beyond the limit so oversized responses are detected even
	// when the server omits Content-Length.
	bytesWritten, err := io.Copy(temporary, io.LimitReader(resp.Body, maxLogoSize+1))
	closeErr := temporary.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if bytesWritten == 0 {
		return "", fmt.Errorf("logo response was empty")
	}
	if bytesWritten > maxLogoSize {
		return "", fmt.Errorf("logo response exceeds %d bytes", maxLogoSize)
	}
	if resp.ContentLength >= 0 && bytesWritten != resp.ContentLength {
		return "", fmt.Errorf("incomplete logo download: got %d bytes, expected %d", bytesWritten, resp.ContentLength)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", err
	}
	keep = true
	_ = os.Chmod(target, 0o644)
	return target, nil
}

func playStoreIconURL(page string) string {
	metaRE := regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	for _, tag := range metaRE.FindAllString(page, -1) {
		property := htmlAttribute(tag, "property")
		if !strings.EqualFold(property, "og:image") {
			continue
		}
		if icon := cleanURL(htmlAttribute(tag, "content")); icon != "" {
			return icon
		}
	}
	jsonImageRE := regexp.MustCompile(`"image"\s*:\s*"(https://play-lh\.googleusercontent\.com/[^"\\]+)`)
	if match := jsonImageRE.FindStringSubmatch(page); len(match) > 1 {
		return cleanURL(html.UnescapeString(match[1]))
	}
	return ""
}

func htmlAttribute(tag, name string) string {
	match := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(name) + `\s*=\s*["']([^"']*)["']`).FindStringSubmatch(tag)
	if len(match) < 2 {
		return ""
	}
	return html.UnescapeString(match[1])
}

func logoExtension(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0])) {
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "image/jpeg":
		return "jpg"
	default:
		return ""
	}
}

func logoTarget(destination, packageID, extension string) (target, directory string, err error) {
	if destination == "" {
		destination = "."
	}
	info, statErr := os.Stat(destination)
	if statErr == nil && !info.IsDir() {
		return destination, filepath.Dir(destination), nil
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return "", "", statErr
	}
	isDirectory := statErr == nil || filepath.Ext(destination) == "" || strings.HasSuffix(destination, "/") || strings.HasSuffix(destination, string(filepath.Separator))
	if isDirectory {
		return filepath.Join(destination, "logo-"+sanitizeFilename(packageID)+"."+extension), destination, nil
	}
	return destination, filepath.Dir(destination), nil
}
