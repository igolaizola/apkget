# 📦 apkget

`apkget` is a Go APK downloader that accepts either an Android package ID or an app name. App names are resolved to the first matching Google Play result, using the same HTML-search approach as the existing helper script.

✨ Highlights:

- 🔎 Resolve app names to package IDs automatically.
- 🧭 Choose a provider and version explicitly, or use automatic fallback.
- 📦 Preserve APK, XAPK, APKS, and APKM artifacts as downloaded.
- 🛡️ Download with SHA-256 verification and no browser automation.

## 📥 Installation

Download a prebuilt binary for your operating system from the [GitHub Releases](https://github.com/igolaizola/apkget/releases/) page.

Or install the latest version with Go:

```sh
go install github.com/igolaizola/apkget/cmd/apkget@latest
```

## 🚀 Usage

```sh
# 🔎 Resolve “Signal” through Google Play, then use source fallback.
apkget -output ./apks Signal

# Write to an explicit file path.
apkget -output ./apks/telegram.apk org.telegram.messenger

# 🆔 A package ID skips the search step.
apkget -output ./apks org.thoughtcrime.securesms

# 🎯 Pin a source or request a specific version.
apkget -source apkpure -version 12.9.2 org.telegram.messenger

# 📋 Inspect sources and versions.
apkget sources
apkget list -source apkpure tiktok
apkget list org.telegram.messenger
apkget logo -output ./logos telegram
```

`list` emits pretty-printed JSON grouped by source and does not download an artifact. It is the inspection command for an app name or package ID; each source contains a `versions` array. Use a returned `source` and one of its versions with `-source` and `-version` to select a specific entry.

`-output` accepts either an output directory or an explicit output file path. The download command shows a terminal progress bar with percentage, speed, and ETA when attached to an interactive terminal. It stays quiet when output is redirected or otherwise not attached to a terminal.

`logo` resolves the app through Google Play and downloads its icon without downloading the APK. The `-output` value may be an output directory or an image file path.

## 🧭 Providers

The default fallback order is:

1. APKPure (the public version/download extraction used by apkeep)
2. APKCombo
3. Uptodown
4. F-Droid
5. APKMirror
6. APK20

## 📦 Artifacts

Downloads are written through a `.part` file and receive a SHA-256 digest. Some providers return XAPK/APKS/APKM bundles instead of a standalone APK. These artifacts are preserved exactly as downloaded; split bundles should be installed as a group with a split-aware Android tool.

## 🌐 Networking

The default HTTP client uses `enetx/surf` Chrome TLS impersonation. Pass a proxy explicitly when needed:

```sh
apkget -proxy http://user:password@proxy.example:8080 tiktok
apkget list -proxy socks5://127.0.0.1:1080 telegram
```

Library users can call `apkget.NewHTTPClient(proxyURL)` or set `Options.Proxy`.

## 📚 Library

```go
ctx := context.Background()
d := apkget.NewDownloader(nil, nil)
result, err := d.Download(ctx, "Signal", apkget.Options{
    OutputDir: "./apks",
})
```

Pass a custom `[]apkget.Source` to `NewDownloader` for tests or a private source. The downloader always preserves the artifact returned by the provider.

Use reasonable request rates and follow the terms and licensing conditions of the app distributor and application publishers.

## 🛠️ Development

```sh
go test ./...
go vet ./...
make build
```

The source implementations are ports/adaptations of the public workflows in [EFForg/apkeep](https://github.com/EFForg/apkeep) and [TheQmaks/justapk](https://github.com/TheQmaks/justapk). This project remains under its existing repository license; review upstream licenses before redistributing derived source code.
