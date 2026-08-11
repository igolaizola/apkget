package apkget

import "testing"

func TestPlayStoreIconURL(t *testing.T) {
	page := `<meta content="https://play-lh.googleusercontent.com/icon&amp;size=512" property="og:image"><script>{"image":"https://play-lh.googleusercontent.com/fallback"}</script>`
	want := "https://play-lh.googleusercontent.com/icon&size=512"
	if got := playStoreIconURL(page); got != want {
		t.Fatalf("icon URL = %q, want %q", got, want)
	}
}

func TestLogoExtension(t *testing.T) {
	for contentType, want := range map[string]string{
		"image/png":           "png",
		"image/jpeg; charset": "jpg",
		"image/webp":          "webp",
		"text/html":           "",
	} {
		if got := logoExtension(contentType); got != want {
			t.Errorf("logoExtension(%q) = %q, want %q", contentType, got, want)
		}
	}
}
