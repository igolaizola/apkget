package apkget

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Downloader coordinates package resolution, source fallback, and XAPK
// handling. Sources are tried in order unless Options.Source pins one.
type Downloader struct {
	client  *http.Client
	sources []Source
}

// NewDownloader creates a downloader. A nil source list selects the default
// justapk-compatible source order.
func NewDownloader(client *http.Client, sources []Source) *Downloader {
	client = defaultClient(client)
	if sources == nil {
		sources = DefaultSources(client)
	}
	return &Downloader{client: client, sources: sources}
}

// Sources returns the configured source names.
func (d *Downloader) Sources() []string {
	names := make([]string, 0, len(d.sources))
	for _, source := range d.sources {
		names = append(names, source.Name())
	}
	return names
}

// ListVersions returns the versions exposed by the selected source(s). A
// source failure is ignored when another source returns data, allowing this
// command to remain useful while providers are intermittently unavailable.
func (d *Downloader) ListVersions(ctx context.Context, packageName, sourceName string) ([]VersionInfo, error) {
	if d == nil || len(d.sources) == 0 {
		return nil, errors.New("no download sources configured")
	}
	var sources []Source
	if sourceName != "" {
		for _, source := range d.sources {
			if source.Name() == sourceName {
				sources = append(sources, source)
			}
		}
		if len(sources) == 0 {
			return nil, fmt.Errorf("unknown source %q (available: %s)", sourceName, strings.Join(d.Sources(), ", "))
		}
	} else {
		sources = d.sources
	}

	var versions []VersionInfo
	var failures []string
	groups := map[string]int{}
	seenVersions := map[string]map[string]bool{}
	for _, source := range sources {
		// Providers opt into version listing independently; one unavailable
		// provider should not hide versions returned by the others.
		lister, ok := source.(VersionLister)
		if !ok {
			failures = append(failures, source.Name()+": version listing is not supported")
			continue
		}
		items, err := lister.ListVersions(ctx, packageName)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", source.Name(), err))
			continue
		}
		for _, item := range items {
			// Custom sources may omit these fields, so normalize them before
			// combining their results with the built-in providers.
			if item.Package == "" {
				item.Package = packageName
			}
			if item.Source == "" {
				item.Source = source.Name()
			}
			if len(item.Versions) == 0 {
				continue
			}
			key := item.Source + "\x00" + item.Package
			index, ok := groups[key]
			if !ok {
				// Keep one JSON object per source/package pair and preserve the
				// order in which providers were queried.
				groups[key] = len(versions)
				versions = append(versions, VersionInfo{Package: item.Package, Source: item.Source})
				index = len(versions) - 1
				seenVersions[key] = map[string]bool{}
			}
			seen := seenVersions[key]
			for _, version := range item.Versions {
				// A provider can expose the same release in multiple records;
				// deduplicate it without sorting away the provider's order.
				if version != "" && !seen[version] {
					versions[index].Versions = append(versions[index].Versions, version)
					seen[version] = true
				}
			}
		}
	}
	if len(versions) == 0 {
		if len(failures) == 0 {
			return nil, fmt.Errorf("no versions found for %s", packageName)
		}
		return nil, fmt.Errorf("could not list versions for %s: %s", packageName, strings.Join(failures, "; "))
	}
	return versions, nil
}

// Download resolves query when needed, then attempts the selected source or
// falls back through every configured source.
func (d *Downloader) Download(ctx context.Context, query string, opts Options) (DownloadResult, error) {
	if d == nil || len(d.sources) == 0 {
		return DownloadResult{}, errors.New("no download sources configured")
	}
	dir, err := outputDir(opts)
	if err != nil {
		return DownloadResult{}, err
	}
	// An explicitly supplied client wins. Otherwise a per-download proxy
	// replaces the downloader's default client for every request in this run.
	client := opts.Client
	if client == nil {
		client = d.client
		if opts.Proxy != "" {
			client = NewHTTPClient(opts.Proxy)
		}
	}
	ctx = withDownloadSettings(ctx, downloadSettings{
		// Request settings travel through context because Source.Download has
		// a deliberately small interface and cannot accept Options directly.
		client:    client,
		userAgent: opts.UserAgent,
		headers:   opts.DownloadHeader,
	})
	packageName := query
	if !LooksLikePackageID(packageName) {
		packageName, err = ResolvePackageID(ctx, query, downloadClient(ctx, d.client))
		if err != nil {
			return DownloadResult{}, err
		}
	}
	sources := d.sources
	if opts.Source != "" {
		sources = nil
		for _, source := range d.sources {
			if source.Name() == opts.Source {
				sources = append(sources, source)
			}
		}
		if len(sources) == 0 {
			return DownloadResult{}, fmt.Errorf("unknown source %q (available: %s)", opts.Source, strings.Join(d.Sources(), ", "))
		}
	}

	var failures []string
	for _, source := range sources {
		// Keep trying after a provider failure; public APK sites frequently
		// block or rate-limit independently of one another.
		fmt.Fprintf(os.Stderr, "Trying %s for %s...\n", source.Name(), packageName)
		result, sourceErr := source.Download(ctx, packageName, dir, opts.Version)
		if sourceErr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", source.Name(), sourceErr))
			fmt.Fprintf(os.Stderr, "  %s failed: %v\n", source.Name(), sourceErr)
			continue
		}
		// Providers may return APK, XAPK, APKS, or APKM artifacts. Preserve the
		// original file because split bundles must be installed as a group.
		return result, nil
	}
	return DownloadResult{}, fmt.Errorf("all sources failed for %s: %s", packageName, strings.Join(failures, "; "))
}

// Search delegates an application-name search to all sources and de-duplicates
// results by package/source.
func (d *Downloader) Search(ctx context.Context, query, sourceName string) ([]AppInfo, error) {
	var sources []Source
	if sourceName != "" {
		for _, source := range d.sources {
			if source.Name() == sourceName {
				sources = append(sources, source)
			}
		}
		if len(sources) == 0 {
			return nil, fmt.Errorf("unknown source %q", sourceName)
		}
	} else {
		sources = d.sources
	}
	seen := map[string]bool{}
	var results []AppInfo
	for _, source := range sources {
		apps, err := source.Search(ctx, query)
		if err != nil {
			continue
		}
		for _, app := range apps {
			key := app.Package + "\x00" + app.Source
			if !seen[key] {
				seen[key] = true
				results = append(results, app)
			}
		}
	}
	return results, nil
}

// Info returns the first metadata record available for a package.
func (d *Downloader) Info(ctx context.Context, packageName, sourceName string) (*AppInfo, error) {
	for _, source := range d.sources {
		if sourceName != "" && source.Name() != sourceName {
			continue
		}
		info, err := source.Info(ctx, packageName)
		if err == nil && info != nil {
			return info, nil
		}
	}
	return nil, nil
}

func outputDir(opts Options) (string, error) {
	dir := opts.OutputDir
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	return filepath.Abs(dir)
}
