package apkget

import (
	"context"
	"net/http"
)

type downloadSettings struct {
	// client is the per-download transport, including any requested proxy.
	client    *http.Client
	userAgent string
	headers   http.Header
}

func downloadClient(ctx context.Context, fallback *http.Client) *http.Client {
	// Context settings override the source's construction-time client. This
	// is what lets one Downloader serve downloads through different proxies.
	if client := downloadSettingsFromContext(ctx).client; client != nil {
		return client
	}
	return defaultClient(fallback)
}

type downloadSettingsKey struct{}

func withDownloadSettings(ctx context.Context, settings downloadSettings) context.Context {
	// Context avoids expanding the Source interface for options that apply to
	// all providers and all requests in one operation.
	return context.WithValue(ctx, downloadSettingsKey{}, settings)
}

func downloadSettingsFromContext(ctx context.Context) downloadSettings {
	if ctx == nil {
		return downloadSettings{}
	}
	settings, _ := ctx.Value(downloadSettingsKey{}).(downloadSettings)
	return settings
}
