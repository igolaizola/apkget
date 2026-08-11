package apkget

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var packageIDRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
var playLinkRE = regexp.MustCompile(`(?i)(?:/store/apps/details\?id=|/store/apps/details\?[^"'<>]*?id=)([A-Za-z][A-Za-z0-9_]*(?:\.[A-Za-z][A-Za-z0-9_]*)+)`)

// LooksLikePackageID applies the same package-name convention as the
// helper script used by the surrounding APK lab.
func LooksLikePackageID(value string) bool { return packageIDRE.MatchString(strings.TrimSpace(value)) }

// ResolvePackageID returns a package ID directly or resolves an app name using
// the first Google Play search result, matching the helper script's behavior.
func ResolvePackageID(ctx context.Context, query string, client *http.Client) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", errors.New("empty app name or package ID")
	}
	if LooksLikePackageID(query) {
		return query, nil
	}
	client = defaultClient(client)
	searchURL := "https://play.google.com/store/search?" + url.Values{
		"q": []string{query}, "c": []string{"apps"}, "hl": []string{"en"}, "gl": []string{"us"},
	}.Encode()
	// Google Play's search page is intentionally parsed as HTML rather than
	// automated-browser content; provider pages only need the package link.
	body, _, err := getText(ctx, client, searchURL, http.Header{"User-Agent": []string{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/124 Safari/537.36"}})
	if err != nil {
		return "", fmt.Errorf("search Google Play for %q: %w", query, err)
	}
	for _, match := range playLinkRE.FindAllStringSubmatch(html.UnescapeString(body), -1) {
		if len(match) > 1 && LooksLikePackageID(match[1]) {
			return match[1], nil
		}
	}
	// Google occasionally escapes the query separator in serialized HTML, so
	// retry against a minimally decoded copy before reporting failure.
	decoded := strings.ReplaceAll(html.UnescapeString(body), `\u003d`, "=")
	for _, match := range playLinkRE.FindAllStringSubmatch(decoded, -1) {
		if len(match) > 1 && LooksLikePackageID(match[1]) {
			return match[1], nil
		}
	}
	return "", fmt.Errorf("no package ID found for %q", query)
}
