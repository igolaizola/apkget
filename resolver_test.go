package apkget

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolvePackageIDDirectAndSearch(t *testing.T) {
	if got, err := ResolvePackageID(context.Background(), "com.example.demo", nil); err != nil || got != "com.example.demo" {
		t.Fatalf("direct package ID = %q, %v", got, err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.URL.RawQuery, "q=Signal") || r.URL.Query().Get("c") != "apps" {
			t.Fatalf("unexpected search URL: %s", r.URL)
		}
		body := `<a href="/store/apps/details?id=org.thoughtcrime.securesms&hl=en">Signal</a>`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	got, err := ResolvePackageID(context.Background(), "Signal", client)
	if err != nil || got != "org.thoughtcrime.securesms" {
		t.Fatalf("searched package ID = %q, %v", got, err)
	}
}

func TestLooksLikePackageIDMatchesHelperScript(t *testing.T) {
	valid := []string{"com.google.android.apps.maps", "org.example_2.app"}
	invalid := []string{"Maps", "com", "1com.example", "com.example-app"}
	for _, value := range valid {
		if !LooksLikePackageID(value) {
			t.Errorf("expected valid package ID %q", value)
		}
	}
	for _, value := range invalid {
		if LooksLikePackageID(value) {
			t.Errorf("expected invalid package ID %q", value)
		}
	}
}
