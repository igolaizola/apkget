package apkget

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type sourceBase struct {
	client *http.Client
}

func (s sourceBase) get(ctx context.Context, rawURL string, headers http.Header) (string, *http.Response, error) {
	// All provider GETs pass through the context-selected client, which keeps
	// proxy and request-header behavior consistent across sources.
	return getText(ctx, downloadClient(ctx, s.client), rawURL, headers)
}

func (s sourceBase) download(ctx context.Context, rawURL, output string, headers http.Header) (int64, string, error) {
	return downloadFile(ctx, downloadClient(ctx, s.client), rawURL, output, headers)
}

// DefaultSources orders providers by current observed success rate and
// coverage for ordinary app queries. Each source is public and may change
// independently, so failures still trigger fallback.
func DefaultSources(client *http.Client) []Source {
	client = defaultClient(client)
	// Ordering matters: Download stops at the first success, while list/search
	// still query every provider that can answer.
	return []Source{
		&APKPureSource{sourceBase: sourceBase{client: client}},
		&APKComboSource{sourceBase: sourceBase{client: client}},
		&UptodownSource{sourceBase: sourceBase{client: client}},
		&FDroidSource{sourceBase: sourceBase{client: client}},
		&APKMirrorSource{sourceBase: sourceBase{client: client}},
		&APK20Source{sourceBase: sourceBase{client: client}},
	}
}

func packageInfo(source, packageName, name, version string) *AppInfo {
	if name == "" {
		name = packageName
	}
	return &AppInfo{Package: packageName, Name: name, Version: version, Source: source}
}

// APKPureSource ports apkeep's public APKPure version endpoint and download
// URL extraction. APKPure calls APK bundles XAPKJ; those are saved as XAPK.
type APKPureSource struct{ sourceBase }

func (*APKPureSource) Name() string { return "apkpure" }
func (s *APKPureSource) ListVersions(ctx context.Context, packageName string) ([]VersionInfo, error) {
	// APKPure exposes version records through a compact API response rather
	// than a normal HTML page, so extract the release keys from that payload.
	body, _, err := s.get(ctx, "https://api.pureapk.com/m/v3/cms/app_version?hl=en-US&package_name="+url.QueryEscape(packageName), http.Header{
		"x-cv": {"3172501"}, "x-sv": {"29"},
		"x-abis": {"arm64-v8a,armeabi-v7a,armeabi,x86,x86_64"}, "x-gp": {"1"},
	})
	if err != nil {
		return nil, fmt.Errorf("[apkpure] versions: %w", err)
	}
	versionRE := regexp.MustCompile(`([[:alnum:].-]+):\([[:xdigit:]]{40,}`)
	seen := map[string]bool{}
	var versions []string
	for _, match := range versionRE.FindAllStringSubmatch(body, -1) {
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		versions = append(versions, match[1])
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("[apkpure] no versions found for %s", packageName)
	}
	return []VersionInfo{{Package: packageName, Source: s.Name(), Versions: versions}}, nil
}
func (s *APKPureSource) Search(ctx context.Context, query string) ([]AppInfo, error) {
	body, _, err := s.get(ctx, "https://apkpure.com/search?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	var apps []AppInfo
	seen := map[string]bool{}
	for _, href := range hrefs(body) {
		parts := strings.Split(strings.Trim(href, "/"), "/")
		if len(parts) < 2 {
			continue
		}
		pkg := parts[len(parts)-1]
		if LooksLikePackageID(pkg) && !seen[pkg] {
			seen[pkg] = true
			apps = append(apps, *packageInfo(s.Name(), pkg, parts[len(parts)-2], ""))
		}
	}
	return apps, nil
}
func (s *APKPureSource) Info(context.Context, string) (*AppInfo, error) { return nil, nil }
func (s *APKPureSource) Download(ctx context.Context, packageName, dir, version string) (DownloadResult, error) {
	rawURL := "https://api.pureapk.com/m/v3/cms/app_version?hl=en-US&package_name=" + url.QueryEscape(packageName)
	headers := http.Header{
		"x-cv": {"3172501"}, "x-sv": {"29"},
		"x-abis": {"arm64-v8a,armeabi-v7a,armeabi,x86,x86_64"}, "x-gp": {"1"},
	}
	body, _, err := s.get(ctx, rawURL, headers)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("[apkpure] versions: %w", err)
	}
	linkRE := regexp.MustCompile(`(?s)(X?APKJ)..(https?://(www\.)?[-a-zA-Z0-9@:%._\+~#=]{1,256}\.[a-zA-Z0-9()]{1,6}\b([-a-zA-Z0-9()@:%_\+.~#?&//=]*))`)
	matchRE := linkRE
	if version != "" {
		// This mirrors apkeep's version-scoped expression: the version is a
		// JSON-like key immediately before the APKPure artifact record.
		matchRE = regexp.MustCompile(`(?s)[^0-9]` + regexp.QuoteMeta(version) + `:(?s:.*?)` + linkRE.String())
	}
	match := matchRE.FindStringSubmatch(body)
	var downloadURL, kind string
	if len(match) >= 3 {
		// The version prefix adds no capture groups. The URL pattern captures
		// kind as group 1 and the complete URL as group 2.
		kind, downloadURL = strings.ToUpper(match[1]), cleanURL(match[2])
	}
	if downloadURL == "" {
		return DownloadResult{}, fmt.Errorf("[apkpure] no download URL for %s", packageName)
	}
	// APKPure calls its split bundle format XAPKJ; normalize that provider
	// spelling to the XAPK extension used by the rest of the application.
	if version == "" {
		version = "latest"
	}
	ext := "apk"
	if kind == "XAPKJ" {
		ext = "xapk"
	}
	path := filepath.Join(dir, sanitizeFilename(packageName)+"-"+sanitizeFilename(version)+"."+ext)
	size, sum, err := s.download(ctx, downloadURL, path, nil)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("[apkpure] download: %w", err)
	}
	return sourceResult(s.Name(), packageName, version, path, size, sum), nil
}

// FDroidSource uses F-Droid's JSON API and predictable repository filenames.
type FDroidSource struct{ sourceBase }

func (*FDroidSource) Name() string { return "fdroid" }
func (s *FDroidSource) ListVersions(ctx context.Context, packageName string) ([]VersionInfo, error) {
	// F-Droid publishes all releases in one package JSON document, so retain
	// the repository's version names while discarding its internal structure.
	data, err := s.packageJSON(ctx, packageName)
	if err != nil {
		return nil, err
	}
	packages, _ := data["packages"].([]any)
	versions := make([]string, 0, len(packages))
	for _, raw := range packages {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["versionName"].(string)
		code, _ := item["versionCode"].(float64)
		if name == "" {
			name = strconv.FormatInt(int64(code), 10)
		}
		versions = append(versions, name)
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("[fdroid] no versions found for %s", packageName)
	}
	return []VersionInfo{{Package: packageName, Source: s.Name(), Versions: versions}}, nil
}
func (s *FDroidSource) Search(ctx context.Context, query string) ([]AppInfo, error) {
	body, _, err := s.get(ctx, "https://search.f-droid.org/?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	var apps []AppInfo
	seen := map[string]bool{}
	for _, href := range hrefs(body) {
		match := regexp.MustCompile(`/packages/([^/?#]+)`).FindStringSubmatch(href)
		if len(match) > 1 && !seen[match[1]] {
			seen[match[1]] = true
			apps = append(apps, *packageInfo(s.Name(), match[1], match[1], ""))
		}
	}
	return apps, nil
}
func (s *FDroidSource) Info(ctx context.Context, packageName string) (*AppInfo, error) {
	data, err := s.packageJSON(ctx, packageName)
	if err != nil {
		return nil, err
	}
	packages, _ := data["packages"].([]any)
	version := ""
	if len(packages) > 0 {
		if item, ok := packages[0].(map[string]any); ok {
			version, _ = item["versionName"].(string)
		}
	}
	return packageInfo(s.Name(), packageName, packageName, version), nil
}
func (s *FDroidSource) packageJSON(ctx context.Context, packageName string) (map[string]any, error) {
	body, resp, err := s.get(ctx, "https://f-droid.org/api/v1/packages/"+url.PathEscape(packageName), nil)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("[fdroid] package not found: %s", packageName)
		}
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, err
	}
	return data, nil
}
func (s *FDroidSource) Download(ctx context.Context, packageName, dir, version string) (DownloadResult, error) {
	data, err := s.packageJSON(ctx, packageName)
	if err != nil {
		return DownloadResult{}, err
	}
	packages, _ := data["packages"].([]any)
	if len(packages) == 0 {
		return DownloadResult{}, fmt.Errorf("[fdroid] no versions for %s", packageName)
	}
	var selected map[string]any
	suggested, _ := data["suggestedVersionCode"].(float64)
	for _, raw := range packages {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["versionName"].(string)
		code, _ := item["versionCode"].(float64)
		if version != "" && name == version {
			selected = item
			break
		}
		if version == "" && selected == nil && (suggested == 0 || code == suggested) {
			selected = item
		}
	}
	if selected == nil {
		return DownloadResult{}, fmt.Errorf("[fdroid] version %q not found for %s", version, packageName)
	}
	code := int64(selected["versionCode"].(float64))
	name, _ := selected["versionName"].(string)
	if name == "" {
		name = strconv.FormatInt(code, 10)
	}
	path := filepath.Join(dir, sanitizeFilename(packageName)+"-"+sanitizeFilename(name)+".apk")
	size, sum, err := s.download(ctx, fmt.Sprintf("https://f-droid.org/repo/%s_%d.apk", packageName, code), path, nil)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("[fdroid] download: %w", err)
	}
	return sourceResult(s.Name(), packageName, name, path, size, sum), nil
}

// APK20Source ports the Next.js/RSC and verification flow used by justapk.
type APK20Source struct{ sourceBase }

func (*APK20Source) Name() string { return "apk20" }
func (s *APK20Source) ListVersions(ctx context.Context, packageName string) ([]VersionInfo, error) {
	body, resp, err := s.get(ctx, "https://www.apk20.com/apk/"+url.PathEscape(packageName), nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("[apk20] package not found: %s", packageName)
	}
	version := metaContent(body, "softwareVersion")
	if version == "" {
		return nil, fmt.Errorf("[apk20] no version found for %s", packageName)
	}
	return []VersionInfo{{Package: packageName, Source: s.Name(), Versions: []string{version}}}, nil
}
func (s *APK20Source) Search(ctx context.Context, query string) ([]AppInfo, error) {
	body, _, err := s.get(ctx, "https://www.apk20.com/search/"+url.PathEscape(query), nil)
	if err != nil {
		return nil, err
	}
	var apps []AppInfo
	seen := map[string]bool{}
	for _, match := range regexp.MustCompile(`"packageName"\s*:\s*"([^"]+)"(?:[^{}]*"title"\s*:\s*"([^"]*)")?`).FindAllStringSubmatch(body, -1) {
		if len(match) > 1 && !seen[match[1]] {
			seen[match[1]] = true
			name := match[1]
			if len(match) > 2 && match[2] != "" {
				name = match[2]
			}
			apps = append(apps, *packageInfo(s.Name(), match[1], name, ""))
		}
	}
	return apps, nil
}
func (s *APK20Source) Info(ctx context.Context, packageName string) (*AppInfo, error) {
	body, resp, err := s.get(ctx, "https://www.apk20.com/apk/"+url.PathEscape(packageName), nil)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	return packageInfo(s.Name(), packageName, metaContent(body, "name"), metaContent(body, "softwareVersion")), nil
}
func (s *APK20Source) Download(ctx context.Context, packageName, dir, version string) (DownloadResult, error) {
	body, resp, err := s.get(ctx, "https://www.apk20.com/apk/"+url.PathEscape(packageName), nil)
	if err != nil {
		return DownloadResult{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return DownloadResult{}, fmt.Errorf("[apk20] package not found: %s", packageName)
	}
	codeRE := regexp.MustCompile(`/apk/` + regexp.QuoteMeta(packageName) + `/download/(\d+)`)
	match := codeRE.FindStringSubmatch(body)
	code := ""
	if len(match) > 1 {
		code = match[1]
	}
	if code == "" {
		match = regexp.MustCompile(`"versionCode"\s*:\s*(\d+)`).FindStringSubmatch(body)
		if len(match) > 1 {
			code = match[1]
		}
	}
	// APK20 requires a verification request before its final file URL is
	// usable; the returned filename is the download target, not the version.
	if code == "" {
		return DownloadResult{}, fmt.Errorf("[apk20] no versions found for %s", packageName)
	}
	availableVersion := metaContent(body, "softwareVersion")
	if version != "" && availableVersion != "" && version != availableVersion {
		return DownloadResult{}, fmt.Errorf("[apk20] version %s not found; available version is %s", version, availableVersion)
	}
	if version == "" {
		version = availableVersion
		if version == "" {
			version = code
		}
	}
	verifyBody, _, err := s.get(ctx, fmt.Sprintf("https://www.apk20.com/api/verify/%s/%s", url.PathEscape(packageName), code), nil)
	if err != nil {
		return DownloadResult{}, err
	}
	var verified struct {
		Success  bool   `json:"success"`
		Filename string `json:"filename"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(verifyBody), &verified); err != nil || !verified.Success || verified.Filename == "" {
		return DownloadResult{}, fmt.Errorf("[apk20] verify failed: %s", verified.Message)
	}
	path := filepath.Join(dir, sanitizeFilename(packageName)+"-"+sanitizeFilename(version)+".apk")
	size, sum, err := s.download(ctx, "https://srv01.apk20.com/"+strings.TrimLeft(verified.Filename, "/"), path, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	return sourceResult(s.Name(), packageName, version, path, size, sum), nil
}

// UptodownSource uses Uptodown's Android API for both metadata and file URLs.
// The public web pages are still useful for selecting an exact XAPK version,
// but resolving the file URL through the Android API avoids the web Turnstile.
type UptodownSource struct {
	sourceBase
	tokenMu         sync.Mutex
	token           string
	tokenValidUntil time.Time
}

func (*UptodownSource) Name() string { return "uptodown" }

const (
	uptodownAPIBase       = "https://www.uptodown.app/eapi"
	uptodownClientVersion = "736"
	// This is the public Android client's native auth key. It is used only to
	// sign the short-lived auth-token request; the bearer token is never stored
	// on disk.
	uptodownAuthAPIKey = "MDGMXUMdvHJBG/vjdFgmqX6LUdy7ecfwvYNd0gyfOCs="
)

func uptodownMobileHeaders() http.Header {
	return http.Header{
		"Accept":                {"application/json"},
		"Connection":            {"Keep-Alive"},
		"Identificador":         {"Uptodown_Android"},
		"Identificador-Version": {uptodownClientVersion},
		"User-Agent":            {"Dalvik/2.1.0 (Linux; U; Android 14; SM-G955F Build/AP2A.240805.005)"},
	}
}

func (s *UptodownSource) accessToken(ctx context.Context) (string, error) {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.token != "" && time.Now().Before(s.tokenValidUntil) {
		// Reuse the short-lived token for all requests made by this source.
		return s.token, nil
	}

	// The mobile endpoint authenticates requests with an HMAC of the current
	// Unix timestamp and the public client key.
	unixTime := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(uptodownAuthAPIKey))
	_, _ = mac.Write([]byte(unixTime))
	hmacValue := hex.EncodeToString(mac.Sum(nil))
	body, _, err := postForm(ctx, downloadClient(ctx, s.client), uptodownAPIBase+"/auth/token", url.Values{
		"hmac":     {hmacValue},
		"unixtime": {unixTime},
	}, uptodownMobileHeaders())
	if err != nil {
		return "", fmt.Errorf("uptodown auth token: %w", err)
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil || response.Token == "" {
		return "", errors.New("uptodown auth response did not contain a token")
	}
	// The current endpoint issues a 30-minute JWT. Keep a safety margin so a
	// long download does not begin with an already-expiring bearer token.
	s.token = response.Token
	s.tokenValidUntil = time.Now().Add(20 * time.Minute)
	return s.token, nil
}

func (s *UptodownSource) api(ctx context.Context, path string) (string, *http.Response, error) {
	token, err := s.accessToken(ctx)
	if err != nil {
		return "", nil, err
	}
	headers := uptodownMobileHeaders()
	headers.Set("Authorization", "Bearer "+token)
	body, response, err := s.get(ctx, uptodownAPIBase+path, headers)
	if response != nil && response.StatusCode == http.StatusUnauthorized {
		// A token can expire between metadata and download requests; invalidate
		// it and retry authentication once.
		s.tokenMu.Lock()
		s.token = ""
		s.tokenValidUntil = time.Time{}
		s.tokenMu.Unlock()
		token, authErr := s.accessToken(ctx)
		if authErr != nil {
			// The first request failed because its token was rejected. Return the
			// refresh error so callers can diagnose authentication failures rather
			// than seeing the stale 401 error from the original request.
			return "", response, authErr
		}
		headers.Set("Authorization", "Bearer "+token)
		return s.get(ctx, uptodownAPIBase+path, headers)
	}
	return body, response, err
}
func (s *UptodownSource) ListVersions(ctx context.Context, packageName string) ([]VersionInfo, error) {
	// Uptodown's public versions page provides both release names and whether
	// each artifact is APK or XAPK. The list command intentionally exposes only
	// the release names; the downloader uses the file type internally.
	slug, err := s.resolveWebSlug(ctx, packageName)
	if err != nil {
		return nil, err
	}
	versionsURL := fmt.Sprintf("https://%s.en.uptodown.com/android/versions", slug)
	body, _, err := s.get(ctx, versionsURL, uptodownBrowserHeaders(""))
	if err != nil {
		return nil, fmt.Errorf("[uptodown] versions: %w", err)
	}
	rows, err := parseUptodownVersionsPage(body)
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(rows))
	for _, row := range rows {
		versions = append(versions, row.Version)
	}
	return []VersionInfo{{Package: packageName, Source: s.Name(), Versions: versions}}, nil
}
func (s *UptodownSource) Search(ctx context.Context, query string) ([]AppInfo, error) {
	body, _, err := s.api(ctx, "/v2/apps/search/"+url.PathEscape(query)+"?page[limit]=30&page[offset]=0")
	if err != nil {
		return nil, err
	}
	var data struct {
		Data struct {
			Results []map[string]any `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, err
	}
	apps := make([]AppInfo, 0, len(data.Data.Results))
	for _, item := range data.Data.Results {
		pkg, _ := item["packageName"].(string)
		if pkg == "" {
			pkg, _ = item["packagename"].(string)
		}
		name, _ := item["name"].(string)
		apps = append(apps, *packageInfo(s.Name(), pkg, name, ""))
	}
	return apps, nil
}
func (s *UptodownSource) Info(ctx context.Context, packageName string) (*AppInfo, error) {
	id, err := s.resolveID(ctx, packageName)
	if err != nil {
		return nil, err
	}
	body, _, err := s.api(ctx, "/v3/apps/"+id+"/device/0?countryIsoCode=US")
	if err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, err
	}
	inner, _ := data["data"].(map[string]any)
	if inner == nil {
		inner = data
	}
	pkg, _ := inner["packagename"].(string)
	name, _ := inner["name"].(string)
	version, _ := inner["lastVersion"].(string)
	if version == "" {
		version, _ = inner["lastVersionCode"].(string)
	}
	return packageInfo(s.Name(), firstNonEmpty(pkg, packageName), name, version), nil
}
func (s *UptodownSource) resolveID(ctx context.Context, packageName string) (string, error) {
	body, resp, err := s.api(ctx, "/apps/byPackagename/"+url.PathEscape(packageName))
	if err == nil && resp.StatusCode == http.StatusOK {
		var data map[string]any
		if json.Unmarshal([]byte(body), &data) == nil {
			inner, _ := data["data"].(map[string]any)
			if inner == nil {
				inner = data
			}
			if id := anyString(inner["appID"], inner["id"]); id != "" {
				return id, nil
			}
		}
	}
	body, _, err = s.api(ctx, "/v2/apps/search/"+url.PathEscape(packageName)+"?page[limit]=5&page[offset]=0")
	if err != nil {
		return "", err
	}
	var data struct {
		Data struct {
			Results []map[string]any `json:"results"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(body), &data) != nil {
		return "", errors.New("invalid Uptodown search response")
	}
	for _, item := range data.Data.Results {
		pkg, _ := item["packageName"].(string)
		if pkg == "" {
			pkg, _ = item["packagename"].(string)
		}
		if pkg == packageName {
			if id := anyString(item["appID"], item["id"]); id != "" {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("[uptodown] app not found: %s", packageName)
}
func (s *UptodownSource) Download(ctx context.Context, packageName, dir, version string) (DownloadResult, error) {
	// The public web flow is the direct-token flow used by the Uptodown
	// downloader in apklab. Keep the mobile API as a fallback because Uptodown
	// rotates or rejects its private API independently of the public site.
	webResult, webErr := s.downloadWeb(ctx, packageName, dir, version)
	if webErr == nil {
		return webResult, nil
	}
	apiResult, apiErr := s.downloadAPI(ctx, packageName, dir, version)
	if apiErr == nil {
		return apiResult, nil
	}
	return DownloadResult{}, fmt.Errorf("[uptodown] web: %v; API: %v", webErr, apiErr)
}

func (s *UptodownSource) downloadWeb(ctx context.Context, packageName, dir, version string) (DownloadResult, error) {
	slug, err := s.resolveWebSlug(ctx, packageName)
	if err != nil {
		return DownloadResult{}, err
	}
	versionsURL := fmt.Sprintf("https://%s.en.uptodown.com/android/versions", slug)
	versionsPage, _, err := s.get(ctx, versionsURL, uptodownBrowserHeaders(""))
	if err != nil {
		return DownloadResult{}, fmt.Errorf("fetch versions: %w", err)
	}
	versions, err := parseUptodownVersionsPage(versionsPage)
	if err != nil {
		return DownloadResult{}, err
	}
	var target *uptodownVersion
	for i := range versions {
		// Prefer XAPK for the latest release because it is usually the complete
		// artifact; an explicit version still wins over that preference.
		candidate := &versions[i]
		if version != "" && candidate.Version != version {
			continue
		}
		if target == nil {
			target = candidate
		}
		if strings.EqualFold(candidate.FileType, "xapk") && !strings.EqualFold(target.FileType, "xapk") {
			target = candidate
		}
		if version != "" && strings.EqualFold(candidate.FileType, "xapk") {
			break
		}
	}
	if target == nil {
		if version == "" {
			return DownloadResult{}, fmt.Errorf("no downloadable versions found for %s", packageName)
		}
		return DownloadResult{}, fmt.Errorf("version %s not found for %s", version, packageName)
	}

	ext := strings.ToLower(target.FileType)
	if ext != "apk" && ext != "xapk" {
		ext = "xapk"
	}
	selectedPage := target.DownloadPage
	if ext == "xapk" {
		// Uptodown links the phone APK page and derives the XAPK page with a
		// stable suffix.
		selectedPage = uptodownXAPKVariantPage(selectedPage)
	}
	downloadPage, _, err := s.get(ctx, selectedPage, uptodownBrowserHeaders(versionsURL))
	if err != nil {
		return DownloadResult{}, fmt.Errorf("fetch XAPK download page: %w", err)
	}
	metadata, err := parseUptodownDownloadMetadata(downloadPage)
	if err != nil {
		return DownloadResult{}, err
	}
	directURL := metadata.DirectURL
	if directURL == "" {
		// Current pages expose app/file tokens instead of a direct URL. Resolve
		// those tokens through the authenticated mobile endpoint.
		var apiSHA string
		directURL, apiSHA, err = s.resolveUptodownDownloadURL(ctx, metadata)
		if err != nil {
			return DownloadResult{}, err
		}
		if metadata.SHA256 == "" {
			metadata.SHA256 = apiSHA
		}
	}
	if metadata.FileType != "" && !strings.EqualFold(metadata.FileType, ext) {
		return DownloadResult{}, fmt.Errorf("download page reports %s instead of %s", metadata.FileType, ext)
	}
	if metadata.SHA256 == "" {
		return DownloadResult{}, errors.New("XAPK page did not expose a SHA256")
	}

	path := filepath.Join(dir, sanitizeFilename(packageName)+"-"+sanitizeFilename(target.Version)+"."+ext)
	size, actualSHA, err := s.download(ctx, directURL, path, uptodownBrowserHeaders(selectedPage))
	if err != nil {
		return DownloadResult{}, err
	}
	if ext == "xapk" {
		if err := validateXAPK(path); err != nil {
			_ = os.Remove(path)
			return DownloadResult{}, fmt.Errorf("downloaded artifact is not a valid XAPK: %w", err)
		}
	}
	if !strings.EqualFold(metadata.SHA256, actualSHA) {
		_ = os.Remove(path)
		return DownloadResult{}, fmt.Errorf("XAPK SHA256 mismatch: expected %s, got %s", metadata.SHA256, actualSHA)
	}
	return sourceResult(s.Name(), packageName, target.Version, path, size, actualSHA), nil
}

func (s *UptodownSource) downloadAPI(ctx context.Context, packageName, dir, version string) (DownloadResult, error) {
	deviceID := strings.TrimSpace(os.Getenv("UPTODOWN_ANDROID_ID"))
	if deviceID == "" {
		return DownloadResult{}, errors.New("[uptodown] mobile version fallback requires UPTODOWN_ANDROID_ID; the web version flow is preferred")
	}
	id, err := s.resolveID(ctx, packageName)
	if err != nil {
		return DownloadResult{}, err
	}
	body, _, err := s.api(ctx, "/v3/app/"+id+"/device/"+url.PathEscape(deviceID)+"/compatible/versions?page[limit]=20&page[offset]=0")
	if err != nil {
		return DownloadResult{}, err
	}
	var data struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(body), &data) != nil || len(data.Data) == 0 {
		return DownloadResult{}, errors.New("[uptodown] no versions found")
	}
	target := data.Data[0]
	if version != "" {
		target = nil
		for _, item := range data.Data {
			if v, _ := item["version"].(string); v == version {
				target = item
				break
			}
		}
		if target == nil {
			return DownloadResult{}, fmt.Errorf("[uptodown] version %s not found", version)
		}
	}
	fileID := anyString(target["fileID"], target["fileid"])
	if fileID == "" {
		return DownloadResult{}, errors.New("[uptodown] no file ID")
	}
	ver, _ := target["version"].(string)
	if ver == "" {
		ver = anyString(target["versionCode"])
	}
	ext, _ := target["fileType"].(string)
	if ext == "" {
		ext = "apk"
	}
	ext = strings.ToLower(ext)
	body, _, err = s.api(ctx, "/apps/"+id+"/file/"+fileID+"/downloadUrl?update=0")
	if err != nil {
		return DownloadResult{}, err
	}
	var link struct {
		Data struct {
			URL string `json:"downloadURL"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(body), &link) != nil || link.Data.URL == "" {
		return DownloadResult{}, errors.New("[uptodown] no download URL")
	}
	path := filepath.Join(dir, sanitizeFilename(packageName)+"-"+sanitizeFilename(ver)+"."+sanitizeFilename(ext))
	size, sum, err := s.download(ctx, link.Data.URL, path, http.Header{"User-Agent": {"Dalvik/2.1.0 (Linux; U; Android 14; SM-G955F Build/AP2A.240805.005)"}})
	if err != nil {
		return DownloadResult{}, err
	}
	return sourceResult(s.Name(), packageName, ver, path, size, sum), nil
}

type uptodownVersion struct {
	Version      string
	DownloadPage string
	FileType     string
}

type uptodownDownloadMetadata struct {
	DirectURL string
	FileType  string
	SHA256    string
	AppID     string
	FileID    string
	OnlyXAPK  string
}

func (s *UptodownSource) resolveUptodownDownloadURL(ctx context.Context, metadata uptodownDownloadMetadata) (string, string, error) {
	if metadata.AppID == "" || metadata.FileID == "" {
		return "", "", errors.New("uptodown download metadata has no app or file ID")
	}
	body, _, err := s.api(ctx, "/apps/"+url.PathEscape(metadata.AppID)+"/file/"+url.PathEscape(metadata.FileID)+"/downloadUrl?update=0")
	if err != nil {
		return "", "", fmt.Errorf("request Uptodown mobile download URL: %w", err)
	}
	var response struct {
		Data struct {
			DownloadURL string `json:"downloadURL"`
			SHA256      string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil || response.Data.DownloadURL == "" {
		return "", "", errors.New("uptodown mobile API returned no download URL")
	}
	return cleanURL(response.Data.DownloadURL), strings.ToLower(response.Data.SHA256), nil
}

func uptodownBrowserHeaders(referer string) http.Header {
	headers := http.Header{
		"User-Agent":      {"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/124 Safari/537.36"},
		"Accept":          {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
		"Accept-Language": {"en-US,en;q=0.9"},
	}
	if referer != "" {
		headers.Set("Referer", referer)
	}
	return headers
}

func (s *UptodownSource) resolveWebSlug(ctx context.Context, input string) (string, error) {
	if !LooksLikePackageID(input) {
		if s.webSlugMatches(ctx, input, "") {
			return sanitizeUptodownSlug(input), nil
		}
	}
	if LooksLikePackageID(input) {
		// Some pages omit their package ID from HTML. Search the API for the
		// display name, then validate the corresponding human-readable slug.
		// Some Uptodown app pages, including TikTok, do not render the Android
		// package name in their HTML. Use the authenticated API search to get
		// the display name, then validate the corresponding web slug by name.
		if apps, searchErr := s.Search(ctx, input); searchErr == nil {
			for _, app := range apps {
				if app.Package != input || app.Name == "" {
					continue
				}
				for _, guess := range uptodownSlugGuesses(app.Name) {
					if s.webSlugMatchesName(ctx, guess, app.Name) {
						return guess, nil
					}
				}
			}
		}
	}
	for _, guess := range uptodownSlugGuesses(input) {
		if s.webSlugMatches(ctx, guess, input) {
			return guess, nil
		}
	}

	searchURL := "https://en.uptodown.com/android/search?q=" + url.QueryEscape(input)
	body, response, err := s.get(ctx, searchURL, uptodownBrowserHeaders(""))
	if err == nil {
		if response != nil && response.Request != nil {
			if slug := uptodownSlugFromURL(response.Request.URL.String()); slug != "" && s.webSlugMatches(ctx, slug, input) {
				return slug, nil
			}
		}
		linkRE := regexp.MustCompile(`(?i)https?://([a-z0-9-]+)\.en\.uptodown\.com/android(?:[/?#"']|$)`)
		seen := map[string]bool{}
		for _, match := range linkRE.FindAllStringSubmatch(html.UnescapeString(body), -1) {
			if len(match) < 2 || seen[match[1]] || match[1] == "uptodown-android" {
				continue
			}
			seen[match[1]] = true
			if s.webSlugMatches(ctx, match[1], input) {
				return match[1], nil
			}
		}
	}
	return "", fmt.Errorf("could not determine Uptodown slug for %s", input)
}

func (s *UptodownSource) webSlugMatches(ctx context.Context, slug, packageName string) bool {
	slug = sanitizeUptodownSlug(slug)
	if slug == "" {
		return false
	}
	body, _, err := s.get(ctx, fmt.Sprintf("https://%s.en.uptodown.com/android", slug), uptodownBrowserHeaders(""))
	if err != nil {
		return false
	}
	if packageName == "" {
		return strings.Contains(strings.ToLower(body), "uptodown")
	}
	return strings.Contains(body, packageName)
}

func (s *UptodownSource) webSlugMatchesName(ctx context.Context, slug, name string) bool {
	slug = sanitizeUptodownSlug(slug)
	if slug == "" || name == "" {
		return false
	}
	body, _, err := s.get(ctx, fmt.Sprintf("https://%s.en.uptodown.com/android", slug), uptodownBrowserHeaders(""))
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(body), strings.ToLower(name))
}

func uptodownSlugGuesses(packageName string) []string {
	parts := strings.Split(strings.ToLower(packageName), ".")
	ignored := map[string]bool{"com": true, "org": true, "net": true, "co": true, "io": true, "gov": true, "android": true, "app": true, "mobile": true}
	useful := make([]string, 0, len(parts))
	for _, part := range parts {
		part = sanitizeUptodownSlug(part)
		if part != "" && !ignored[part] {
			useful = append(useful, part)
		}
	}
	guesses := []string{}
	add := func(value string) {
		value = sanitizeUptodownSlug(value)
		if value == "" {
			return
		}
		for _, existing := range guesses {
			if existing == value {
				return
			}
		}
		guesses = append(guesses, value)
	}
	if len(useful) > 0 {
		add(useful[0])
		add(useful[len(useful)-1])
		add(strings.Join(useful, "-"))
	}
	for _, part := range parts {
		add(part)
	}
	return guesses
}

func sanitizeUptodownSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		default:
			if builder.Len() > 0 && !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func uptodownSlugFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	const suffix = ".en.uptodown.com"
	if strings.HasSuffix(host, suffix) {
		return strings.TrimSuffix(host, suffix)
	}
	return ""
}

func parseUptodownVersionsPage(page string) ([]uptodownVersion, error) {
	openRE := regexp.MustCompile(`(?is)<div\b[^>]*\bdata-version-id\s*=\s*["'][^"']+["'][^>]*>`)
	positions := openRE.FindAllStringIndex(page, -1)
	if len(positions) == 0 {
		return nil, errors.New("no Uptodown version rows found")
	}
	versionRE := regexp.MustCompile(`(?is)<span[^>]+class=["'][^"']*\bversion\b[^"']*["'][^>]*>\s*([^<]+)`)
	kindRE := regexp.MustCompile(`(?i)\b(xapk|apk)\b`)
	seen := map[string]bool{}
	versions := make([]uptodownVersion, 0, len(positions))
	for i, position := range positions {
		// Bound each row so a malformed page cannot make one version consume the
		// entire remainder of the document.
		start, end := position[0], len(page)
		if i+1 < len(positions) {
			end = positions[i+1][0]
		} else if end-start > 12000 {
			end = start + 12000
		}
		block := page[start:end]
		openTag := page[position[0]:position[1]]
		versionID := uptodownAttr(openTag, "data-version-id")
		baseURL := uptodownAttr(openTag, "data-url")
		extraURL := uptodownAttr(openTag, "data-extra-url")
		if extraURL == "" {
			extraURL = "download"
		}
		match := versionRE.FindStringSubmatch(block)
		if len(match) < 2 || versionID == "" || baseURL == "" {
			continue
		}
		version := strings.TrimSpace(html.UnescapeString(match[1]))
		key := version + "\x00" + versionID
		if version == "" || seen[key] {
			continue
		}
		seen[key] = true
		fileType := ""
		if kind := kindRE.FindStringSubmatch(strings.ToLower(stripUptodownTags(block))); len(kind) > 1 {
			fileType = strings.ToLower(kind[1])
		}
		downloadPage := strings.TrimRight(html.UnescapeString(baseURL), "/") + "/" + strings.Trim(html.UnescapeString(extraURL), "/") + "/" + url.PathEscape(versionID)
		versions = append(versions, uptodownVersion{Version: version, DownloadPage: downloadPage, FileType: fileType})
	}
	if len(versions) == 0 {
		return nil, errors.New("uptodown version rows could not be parsed")
	}
	return versions, nil
}

func uptodownXAPKVariantPage(rawURL string) string {
	rawURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
	if strings.HasSuffix(rawURL, "-x") {
		return rawURL
	}
	return rawURL + "-x"
}

func parseUptodownDownloadMetadata(page string) (uptodownDownloadMetadata, error) {
	var metadata uptodownDownloadMetadata
	buttonRE := regexp.MustCompile(`(?is)<button\b[^>]*\bid\s*=\s*["']detail-download-button["'][^>]*>`)
	button := buttonRE.FindString(page)
	if button == "" {
		return metadata, errors.New("uptodown download button not found")
	}
	token := uptodownAttr(button, "data-url")
	metadata.AppID = uptodownAttr(button, "data-app-id")
	metadata.FileID = uptodownAttr(button, "data-file-id")
	metadata.OnlyXAPK = uptodownAttr(button, "data-only-xapk")
	if token != "" {
		// Keep support for the legacy direct token, although current downloads
		// generally require app/file IDs and the mobile API.
		metadata.DirectURL = cleanURL("https://dw.uptodown.com/dwn/" + strings.TrimLeft(html.UnescapeString(token), "/"))
	}
	formatRE := regexp.MustCompile(`(?is)<span[^>]+class=["'][^"']*\bformat\b[^"']*["'][^>]*>\s*([^<]+)`)
	if match := formatRE.FindStringSubmatch(page); len(match) > 1 {
		value := strings.ToLower(strings.TrimSpace(stripUptodownTags(match[1])))
		if value == "apk" || value == "xapk" {
			metadata.FileType = value
		}
	}
	if metadata.FileType == "" {
		plain := strings.ToLower(stripUptodownTags(page))
		if strings.Contains(plain, "file type xapk") || strings.Contains(plain, " xapk ") {
			metadata.FileType = "xapk"
		} else if strings.Contains(plain, "file type apk") || strings.Contains(plain, " apk ") {
			metadata.FileType = "apk"
		}
	}
	metadata.SHA256 = findUptodownSHA256(page)
	if metadata.DirectURL == "" && (metadata.AppID == "" || metadata.FileID == "") {
		return metadata, errors.New("uptodown download button has no direct token")
	}
	return metadata, nil
}

func findUptodownSHA256(page string) string {
	lower := strings.ToLower(page)
	hex64 := regexp.MustCompile(`(?i)\b[a-f0-9]{64}\b`)
	for from := 0; from < len(lower); {
		relative := strings.Index(lower[from:], "sha256")
		if relative < 0 {
			break
		}
		index := from + relative
		start, end := index-256, index+2048
		if start < 0 {
			start = 0
		}
		if end > len(page) {
			end = len(page)
		}
		if digest := hex64.FindString(page[start:end]); digest != "" {
			return strings.ToLower(digest)
		}
		from = index + len("sha256")
	}
	return ""
}

func uptodownAttr(tag, name string) string {
	match := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(name) + `\s*=\s*["']([^"']+)["']`).FindStringSubmatch(tag)
	if len(match) < 2 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(match[1]))
}

func stripUptodownTags(value string) string {
	value = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(html.UnescapeString(value)), " ")
}

// APKComboSource and APKMirrorSource keep the HTML fallback paths from
// justapk. They intentionally use small parsers so the binary has no HTML
// parser dependency.
type APKComboSource struct{ sourceBase }

func (*APKComboSource) Name() string { return "apkcombo" }
func (s *APKComboSource) ListVersions(ctx context.Context, packageName string) ([]VersionInfo, error) {
	// APKCombo's search result supplies the app slug; the download page then
	// contains the current and several old releases in vername elements.
	body, _, err := s.get(ctx, "https://apkcombo.com/search/"+url.PathEscape(packageName), nil)
	if err != nil {
		return nil, err
	}
	slug := findComboSlug(body, packageName)
	if slug == "" {
		return nil, fmt.Errorf("[apkcombo] package not found: %s", packageName)
	}
	page, _, err := s.get(ctx, "https://apkcombo.com/"+slug+"/"+url.PathEscape(packageName)+"/download/apk", nil)
	if err != nil {
		return nil, err
	}
	versions := comboVersions(page)
	if len(versions) == 0 {
		return nil, fmt.Errorf("[apkcombo] no version found for %s", packageName)
	}
	return []VersionInfo{{Package: packageName, Source: s.Name(), Versions: versions}}, nil
}
func (s *APKComboSource) Search(ctx context.Context, query string) ([]AppInfo, error) {
	body, _, err := s.get(ctx, "https://apkcombo.com/search/"+url.PathEscape(query), nil)
	if err != nil {
		return nil, err
	}
	var apps []AppInfo
	for _, href := range hrefs(body) {
		parts := strings.Split(strings.Trim(href, "/"), "/")
		if len(parts) >= 2 && LooksLikePackageID(parts[len(parts)-1]) {
			apps = append(apps, *packageInfo(s.Name(), parts[len(parts)-1], parts[0], ""))
		}
	}
	return apps, nil
}
func (s *APKComboSource) Info(context.Context, string) (*AppInfo, error) { return nil, nil }
func (s *APKComboSource) Download(ctx context.Context, packageName, dir, version string) (DownloadResult, error) {
	body, _, err := s.get(ctx, "https://apkcombo.com/search/"+url.PathEscape(packageName), nil)
	if err != nil {
		return DownloadResult{}, err
	}
	slug := findComboSlug(body, packageName)
	if slug == "" {
		return DownloadResult{}, fmt.Errorf("[apkcombo] package not found: %s", packageName)
	}
	page, _, err := s.get(ctx, "https://apkcombo.com/"+slug+"/"+url.PathEscape(packageName)+"/download/apk", nil)
	if err != nil {
		return DownloadResult{}, err
	}
	var dl string
	for _, href := range hrefs(page) {
		if strings.Contains(href, "/r2?") {
			dl = href
			if !strings.HasPrefix(dl, "http") {
				dl = "https://apkcombo.com" + dl
			}
			if !strings.Contains(strings.ToLower(href), "xapk") {
				break
			}
		}
	}
	if dl == "" {
		return DownloadResult{}, errors.New("[apkcombo] no download URL")
	}
	availableVersion := firstNonEmpty(comboVersions(page)...)
	if version != "" && availableVersion != "" && version != availableVersion {
		return DownloadResult{}, fmt.Errorf("[apkcombo] version %s not found; available version is %s", version, availableVersion)
	}
	if version == "" {
		version = firstNonEmpty(availableVersion, "latest")
	}
	ext := "apk"
	if strings.Contains(strings.ToLower(dl), "xapk") {
		ext = "xapk"
	}
	path := filepath.Join(dir, sanitizeFilename(packageName)+"-"+sanitizeFilename(version)+"."+ext)
	size, sum, err := s.download(ctx, html.UnescapeString(dl), path, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	return sourceResult(s.Name(), packageName, version, path, size, sum), nil
}

type APKMirrorSource struct{ sourceBase }

func (*APKMirrorSource) Name() string { return "apkmirror" }
func (s *APKMirrorSource) ListVersions(ctx context.Context, packageName string) ([]VersionInfo, error) {
	// APKMirror requires a search page followed by a release page; both are
	// protected independently, so failures are returned to the aggregator.
	searchURL := "https://www.apkmirror.com/?post_type=app_listing&searchtype=apk&s=" + url.QueryEscape(packageName)
	searchPage, _, err := s.get(ctx, searchURL, nil)
	if err != nil {
		return nil, err
	}
	releaseURL := ""
	for _, anchor := range anchors(searchPage) {
		href := anchorAttribute(anchor, "href")
		if strings.Contains(href, "/apk/") && (strings.Contains(href, packageName) || releaseURL == "") {
			releaseURL = absoluteURL("https://www.apkmirror.com", href)
			if strings.Contains(href, packageName) {
				break
			}
		}
	}
	if releaseURL == "" {
		return nil, fmt.Errorf("[apkmirror] package not found: %s", packageName)
	}
	releasePage, _, err := s.get(ctx, releaseURL, nil)
	if err != nil {
		return nil, err
	}
	version := firstNumericVersion(releasePage)
	if version == "" {
		return nil, fmt.Errorf("[apkmirror] no version found for %s", packageName)
	}
	return []VersionInfo{{Package: packageName, Source: s.Name(), Versions: []string{version}}}, nil
}
func (s *APKMirrorSource) Search(ctx context.Context, query string) ([]AppInfo, error) {
	body, _, err := s.get(ctx, "https://www.apkmirror.com/?post_type=app_listing&searchtype=apk&s="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	var apps []AppInfo
	for _, href := range hrefs(body) {
		if strings.Contains(href, "/apk/") {
			pkg := strings.Trim(strings.TrimSuffix(href, "/"), "/")
			if i := strings.LastIndex(pkg, "/"); i >= 0 {
				pkg = pkg[i+1:]
			}
			apps = append(apps, *packageInfo(s.Name(), pkg, pkg, ""))
		}
	}
	return apps, nil
}
func (s *APKMirrorSource) Info(context.Context, string) (*AppInfo, error) { return nil, nil }
func (s *APKMirrorSource) Download(ctx context.Context, packageName, dir, version string) (DownloadResult, error) {
	searchURL := "https://www.apkmirror.com/?post_type=app_listing&searchtype=apk&s=" + url.QueryEscape(packageName)
	searchPage, _, err := s.get(ctx, searchURL, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	releaseURL := ""
	for _, anchor := range anchors(searchPage) {
		href := anchorAttribute(anchor, "href")
		if strings.Contains(href, "/apk/") && (strings.Contains(href, packageName) || releaseURL == "") {
			releaseURL = absoluteURL("https://www.apkmirror.com", href)
			if strings.Contains(href, packageName) {
				break
			}
		}
	}
	if releaseURL == "" {
		return DownloadResult{}, fmt.Errorf("[apkmirror] package not found: %s", packageName)
	}
	releasePage, _, err := s.get(ctx, releaseURL, nil)
	if err != nil || !strings.Contains(releasePage, packageName) {
		if err != nil {
			return DownloadResult{}, err
		}
		return DownloadResult{}, fmt.Errorf("[apkmirror] package not found: %s", packageName)
	}
	ver := firstNumericVersion(releasePage)
	if version != "" && ver == "" {
		return DownloadResult{}, fmt.Errorf("[apkmirror] could not determine the available version for %s", packageName)
	}
	if version != "" && ver != "" && version != ver {
		return DownloadResult{}, fmt.Errorf("[apkmirror] version %s not found; latest is %s", version, ver)
	}
	variantURL := ""
	for _, anchor := range anchors(releasePage) {
		href := anchorAttribute(anchor, "href")
		if !strings.Contains(strings.ToLower(anchor), "accent_color") || href == "" {
			continue
		}
		variantURL = absoluteURL("https://www.apkmirror.com", href)
		plain := strings.ToLower(stripTags(anchor))
		if strings.Contains(plain, "universal") || strings.Contains(plain, "nodpi") {
			break
		}
	}
	if variantURL == "" {
		return DownloadResult{}, errors.New("[apkmirror] no APK variant found")
	}
	variantPage, _, err := s.get(ctx, variantURL, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	downloadPageURL := ""
	for _, anchor := range anchors(variantPage) {
		if strings.Contains(strings.ToLower(anchor), "downloadbutton") {
			downloadPageURL = absoluteURL("https://www.apkmirror.com", anchorAttribute(anchor, "href"))
			break
		}
	}
	if downloadPageURL == "" {
		return DownloadResult{}, errors.New("[apkmirror] no download button")
	}
	confirmation, _, err := s.get(ctx, downloadPageURL, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	finalURL := ""
	for _, anchor := range anchors(confirmation) {
		href := anchorAttribute(anchor, "href")
		if strings.Contains(href, "key=") && strings.Contains(strings.ToLower(href), "download") {
			finalURL = absoluteURL("https://www.apkmirror.com", href)
			break
		}
	}
	if finalURL == "" {
		return DownloadResult{}, errors.New("[apkmirror] no final download link")
	}
	if ver == "" {
		ver = "latest"
	}
	path := filepath.Join(dir, sanitizeFilename(packageName)+"-"+sanitizeFilename(ver)+".apk")
	size, sum, err := s.download(ctx, finalURL, path, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	return sourceResult(s.Name(), packageName, ver, path, size, sum), nil
}

func hrefs(body string) []string {
	re := regexp.MustCompile(`(?is)href\s*=\s*["']([^"']+)["']`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if len(m) > 1 {
			out = append(out, html.UnescapeString(m[1]))
		}
	}
	return out
}

func anchors(body string) []string {
	return regexp.MustCompile(`(?is)<a\b[^>]*>.*?</a>`).FindAllString(body, -1)
}

func anchorAttribute(anchor, name string) string {
	re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(name) + `\s*=\s*["']([^"']*)["']`)
	match := re.FindStringSubmatch(anchor)
	if len(match) < 2 {
		return ""
	}
	return html.UnescapeString(match[1])
}

func absoluteURL(base, raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	origin, err := url.Parse(base)
	if err != nil {
		return raw
	}
	return origin.ResolveReference(parsed).String()
}

func firstNumericVersion(body string) string {
	match := regexp.MustCompile(`\b(\d+(?:\.\d+){1,4})\b`).FindStringSubmatch(stripTags(body))
	if len(match) > 1 {
		return match[1]
	}
	return ""
}
func findComboSlug(body, packageName string) string {
	for _, href := range hrefs(body) {
		parts := strings.Split(strings.Trim(href, "/"), "/")
		for i, part := range parts {
			if part == packageName && i > 0 {
				return parts[i-1]
			}
		}
	}
	return ""
}
func metaContent(body, property string) string {
	re := regexp.MustCompile(`(?is)<[^>]+itemprop=["']` + regexp.QuoteMeta(property) + `["'][^>]*>`)
	tag := re.FindString(body)
	if tag == "" {
		return ""
	}
	if m := regexp.MustCompile(`(?i)content=["']([^"']*)`).FindStringSubmatch(tag); len(m) > 1 {
		return html.UnescapeString(m[1])
	}
	return stripTags(tag)
}
func comboVersions(body string) []string {
	versionRE := regexp.MustCompile(`(?i)\b(\d+(?:\.\d+){1,4}(?:[-+][[:alnum:].-]+)?)\b`)
	seen := map[string]bool{}
	versions := make([]string, 0, 4)
	add := func(value string) {
		value = strings.TrimSpace(html.UnescapeString(value))
		match := versionRE.FindStringSubmatch(value)
		if len(match) > 1 && !seen[match[1]] {
			seen[match[1]] = true
			versions = append(versions, match[1])
		}
	}

	vernameRE := regexp.MustCompile(`(?is)<[^>]+class=["'][^"']*\bvername\b[^"']*["'][^>]*>([^<]*)`)
	for _, match := range vernameRE.FindAllStringSubmatch(body, -1) {
		if len(match) > 1 {
			add(match[1])
		}
	}
	if len(versions) == 0 {
		metaRE := regexp.MustCompile(`(?i)\bversion\s*:\s*([^<,&]+)`)
		if match := metaRE.FindStringSubmatch(body); len(match) > 1 {
			add(match[1])
		}
	}
	return versions
}
func stripTags(value string) string {
	return strings.TrimSpace(regexp.MustCompile(`(?s)<[^>]*>`).ReplaceAllString(value, ""))
}
func anyString(values ...any) string {
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			if v != 0 {
				return strconv.FormatInt(int64(v), 10)
			}
		case json.Number:
			return v.String()
		}
	}
	return ""
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cleanURL(raw string) string {
	raw = html.UnescapeString(raw)
	if index := strings.IndexByte(raw, '\\'); index >= 0 {
		raw = raw[:index]
	}
	for index, r := range raw {
		if unicode.IsControl(r) {
			raw = raw[:index]
			break
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return raw
}
