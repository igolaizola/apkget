package apkget

import (
	"strings"
	"testing"
)

func TestParseUptodownCurrentDownloadButton(t *testing.T) {
	page := `<button id="detail-download-button" data-app-id="27389" data-file-id="1195820247" data-only-xapk="1" data-download-version="1195820247"><span class="format">XAPK</span></button><div>SHA256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef</div>`
	metadata, err := parseUptodownDownloadMetadata(page)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.DirectURL != "" {
		t.Fatalf("expected no legacy direct URL, got %q", metadata.DirectURL)
	}
	if metadata.AppID != "27389" || metadata.FileID != "1195820247" || metadata.OnlyXAPK != "1" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	if metadata.FileType != "xapk" || metadata.SHA256 == "" {
		t.Fatalf("missing XAPK metadata: %+v", metadata)
	}
}

func TestDefaultSourceOrder(t *testing.T) {
	d := NewDownloader(nil, nil)
	want := []string{"apkpure", "apkcombo", "uptodown", "fdroid", "apkmirror", "apk20"}
	got := d.Sources()
	if len(got) != len(want) {
		t.Fatalf("source count = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("source %d = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
}

func TestComboVersions(t *testing.T) {
	page := `<meta name="description" content="Download idealista APK - Version: 15.2.1 - com.idealista.android"><span class="vername">idealista 15.2.1</span><span class="vername">idealista 15.2.0</span><span class="vername">idealista 15.1.1</span>`
	want := []string{"15.2.1", "15.2.0", "15.1.1"}
	got := comboVersions(page)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("comboVersions() = %v, want %v", got, want)
	}
}

func TestUptodownSlugGuessesFromDisplayName(t *testing.T) {
	guesses := uptodownSlugGuesses("TikTok")
	if len(guesses) == 0 || guesses[0] != "tiktok" {
		t.Fatalf("uptodownSlugGuesses(TikTok) = %v, want tiktok first", guesses)
	}
}
