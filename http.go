package apkget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/enetx/g"
	"github.com/enetx/surf"
)

const defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

func defaultClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	// Keep all providers on the same browser-like client unless the caller
	// supplied a custom transport.
	return NewHTTPClient("")
}

// NewHTTPClient creates the default TLS-impersonating HTTP client. If proxy
// is non-empty, it must be a URL supported by enetx/surf.
func NewHTTPClient(proxy string) *http.Client {
	// surf supplies the TLS/client fingerprint used by sites that reject a
	// plain net/http client. Proxy configuration is applied before building it.
	builder := surf.NewClient().Builder().Timeout(45 * time.Second).Impersonate().Chrome()
	if proxy = strings.TrimSpace(proxy); proxy != "" {
		builder = builder.Proxy(g.String(proxy))
	}
	if built := builder.Build(); built.IsOk() {
		return built.Ok().Std()
	}
	return &http.Client{Timeout: 45 * time.Second}
}

func newRequest(ctx context.Context, method, rawURL string, headers http.Header) (*http.Request, error) {
	return newRequestBody(ctx, method, rawURL, nil, headers)
}

func newRequestBody(ctx context.Context, method, rawURL string, body io.Reader, headers http.Header) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	settings := downloadSettingsFromContext(ctx)
	userAgent := settings.userAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Options.DownloadHeader is applied before source-specific headers so the
	// latter can override defaults such as User-Agent when needed.
	for key, values := range settings.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	for key, values := range headers {
		if len(values) > 0 && strings.EqualFold(key, "User-Agent") {
			req.Header.Del("User-Agent")
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	return req, nil
}

func postForm(ctx context.Context, client *http.Client, rawURL string, values url.Values, headers http.Header) (string, *http.Response, error) {
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "application/x-www-form-urlencoded")
	req, err := newRequestBody(ctx, http.MethodPost, rawURL, strings.NewReader(values.Encode()), headers)
	if err != nil {
		return "", nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return "", resp, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp, fmt.Errorf("HTTP %s: %s", resp.Status, preview(string(responseBody), 300))
	}
	return string(responseBody), resp, nil
}

func getText(ctx context.Context, client *http.Client, rawURL string, headers http.Header) (string, *http.Response, error) {
	req, err := newRequest(ctx, http.MethodGet, rawURL, headers)
	if err != nil {
		return "", nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if readErr != nil {
		return "", resp, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp, fmt.Errorf("HTTP %s: %s", resp.Status, preview(string(body), 300))
	}
	return string(body), resp, nil
}

func downloadFile(ctx context.Context, client *http.Client, rawURL, dest string, headers http.Header) (int64, string, error) {
	req, err := newRequest(ctx, http.MethodGet, rawURL, headers)
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, "", fmt.Errorf("HTTP %s: %s", resp.Status, preview(string(body), 300))
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, "", err
	}
	part := dest + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = os.Remove(part) }()

	// Write the file and hash in one pass; the digest then describes exactly
	// the bytes that reached disk.
	hash := sha256.New()
	writers := []io.Writer{f, hash}
	progress := newProgressBar(filepath.Base(dest), resp.ContentLength, isTerminal(os.Stderr))
	if progress != nil {
		// Progress is an additional writer, so pipes and redirected output stay
		// quiet while interactive terminals receive live updates.
		writers = append(writers, progress)
		progress.start()
		defer progress.finish()
	}
	w := io.MultiWriter(writers...)
	n, copyErr := io.Copy(w, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return n, "", copyErr
	}
	if closeErr != nil {
		return n, "", closeErr
	}
	if n == 0 {
		return 0, "", errors.New("server returned an empty file")
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return n, "", fmt.Errorf("incomplete download: got %d bytes, expected %d", n, resp.ContentLength)
	}
	// Rename only after validation so callers never see a partially downloaded
	// destination file.
	if err := os.Rename(part, dest); err != nil {
		return n, "", err
	}
	return n, hex.EncodeToString(hash.Sum(nil)), nil
}

func preview(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > limit {
		return value[:limit] + "..."
	}
	return value
}

func sanitizeFilename(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "download"
	}
	return b.String()
}

func sourceResult(source, packageName, version, path string, size int64, sum string) DownloadResult {
	return DownloadResult{Path: path, Package: packageName, Version: version, Source: source, Size: size, SHA256: sum}
}
