package apkget

import (
	"context"
	"net/http"
)

// AppInfo describes an application returned by a source search.
type AppInfo struct {
	Package     string `json:"package"`
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	VersionCode int64  `json:"version_code,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Source      string `json:"source"`
	IconURL     string `json:"icon_url,omitempty"`
	Description string `json:"description,omitempty"`
}

// DownloadResult describes a downloaded artifact and its SHA-256 digest.
type DownloadResult struct {
	Path    string `json:"path"`
	Package string `json:"package"`
	Version string `json:"version"`
	Source  string `json:"source"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

// VersionInfo describes the versions advertised by one source. Listing
// versions only fetches provider metadata; it never downloads an artifact.
type VersionInfo struct {
	Package  string   `json:"package"`
	Source   string   `json:"source"`
	Versions []string `json:"versions"`
}

// VersionLister is implemented by sources that can enumerate their public
// version metadata without downloading an APK or bundle.
type VersionLister interface {
	ListVersions(context.Context, string) ([]VersionInfo, error)
}

// Source is one APK provider. Implementations should return an error when a
// package cannot be found so Downloader can continue through its fallback list.
type Source interface {
	Name() string
	Search(context.Context, string) ([]AppInfo, error)
	Info(context.Context, string) (*AppInfo, error)
	Download(context.Context, string, string, string) (DownloadResult, error)
}

// Options controls a download operation.
type Options struct {
	OutputDir      string       // Directory where the provider artifact is written.
	Source         string       // Optional provider name; empty means fallback order.
	Version        string       // Optional exact version requested from the provider.
	Proxy          string       // Optional HTTP/SOCKS proxy URL.
	Client         *http.Client // Optional custom client; takes precedence over Proxy.
	UserAgent      string       // Optional User-Agent applied to provider requests.
	DownloadHeader http.Header  // Additional headers applied to provider requests.
}
