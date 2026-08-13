package apkget

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	apktoolVersion = "2.12.1"
	jadxVersion    = "1.5.3"
	apktoolURL     = "https://github.com/iBotPeaches/Apktool/releases/download/v" + apktoolVersion + "/apktool_" + apktoolVersion + ".jar"
	jadxURL        = "https://github.com/skylot/jadx/releases/download/v" + jadxVersion + "/jadx-" + jadxVersion + ".zip"
	apktoolSHA256  = "66cf4524a4a45a7f56567d08b2c9b6ec237bcdd78cee69fd4a59c8a0243aeafa"
	jadxSHA256     = "8280f3799c0273fe797a2bcd90258c943e451fd195f13d05400de5e6451d15ec"
)

// ReverseOptions controls a reverse-engineering run. The tools are downloaded to
// ToolHome when they are missing; APKLAB_HOME is used when ToolHome is empty.
type ReverseOptions struct {
	OutputDir string
	ToolHome  string
	Proxy     string
	Client    *http.Client
}

// ReverseResult describes the files produced by Reverse.
type ReverseResult struct {
	InputPath  string
	OutputDir  string
	JavaSource string
}

// Reverse extracts resources, smali, and deobfuscated Java-like source from
// an APK or an APK bundle. Bundles are not merged: the best primary APK is
// selected using bundle metadata and fallback heuristics.
func Reverse(ctx context.Context, inputPath string, opts ReverseOptions) (ReverseResult, error) {
	inputPath, err := filepath.Abs(inputPath)
	if err != nil {
		return ReverseResult{}, fmt.Errorf("resolve input path: %w", err)
	}
	info, err := os.Stat(inputPath)
	if err != nil {
		return ReverseResult{}, fmt.Errorf("stat input: %w", err)
	}
	if info.IsDir() {
		return ReverseResult{}, fmt.Errorf("input is a directory: %s", inputPath)
	}
	if !isReverseInput(inputPath) {
		return ReverseResult{}, fmt.Errorf("unsupported input %q; expected .apk, .apkx, .xapk, .apks, or .apkm", inputPath)
	}

	client := opts.Client
	if client == nil {
		client = NewHTTPClient(opts.Proxy)
	}
	tools, err := ensureReverseTools(ctx, opts.ToolHome, client, standardToolClient(opts.Proxy))
	if err != nil {
		return ReverseResult{}, err
	}
	if _, err := exec.LookPath("java"); err != nil {
		return ReverseResult{}, errors.New("java is required for reverse engineering but was not found in PATH")
	}

	workAPK := inputPath
	var temporaryDir string
	if isBundleInput(inputPath) {
		temporaryDir, err = os.MkdirTemp("", "apkget-reverse-*")
		if err != nil {
			return ReverseResult{}, fmt.Errorf("create bundle workspace: %w", err)
		}
		defer func() { _ = os.RemoveAll(temporaryDir) }()
		workAPK, err = selectBundleAPK(inputPath, temporaryDir)
		if err != nil {
			return ReverseResult{}, err
		}
	}

	outputDir := opts.OutputDir
	if outputDir == "" {
		outputDir = filepath.Join(filepath.Dir(inputPath), strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath)))
	}
	outputDir, err = filepath.Abs(outputDir)
	if err != nil {
		return ReverseResult{}, fmt.Errorf("resolve output path: %w", err)
	}

	javaSource := filepath.Join(outputDir, "java_src")
	fmt.Fprintf(os.Stderr, "Decompiling resources with apktool...\n")
	if err := runReverseCommand(ctx, "apktool", tools.apktoolJar, "d", workAPK, "-o", outputDir); err != nil {
		return ReverseResult{}, fmt.Errorf("apktool: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Decompiling Java source with jadx...\n")
	if err := runReverseCommand(ctx, "jadx", tools.jadxPath, "--deobf", "--show-bad-code", "-r", "-ds", javaSource, workAPK); err != nil {
		return ReverseResult{}, fmt.Errorf("jadx: %w", err)
	}

	return ReverseResult{InputPath: inputPath, OutputDir: outputDir, JavaSource: javaSource}, nil
}

type reverseTools struct {
	apktoolJar string
	jadxPath   string
}

func ensureReverseTools(ctx context.Context, toolHome string, client, fallbackClient *http.Client) (reverseTools, error) {
	if toolHome == "" {
		toolHome = os.Getenv("APKLAB_HOME")
	}
	if toolHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return reverseTools{}, fmt.Errorf("resolve user home: %w", err)
		}
		toolHome = filepath.Join(home, ".apklab")
	}
	toolHome, err := filepath.Abs(toolHome)
	if err != nil {
		return reverseTools{}, fmt.Errorf("resolve tool home: %w", err)
	}
	if err := os.MkdirAll(toolHome, 0o755); err != nil {
		return reverseTools{}, fmt.Errorf("create tool home: %w", err)
	}

	apktoolPath := filepath.Join(toolHome, "apktool_"+apktoolVersion+".jar")
	if err := ensureDownloadedTool(ctx, client, fallbackClient, apktoolURL, apktoolPath); err != nil {
		return reverseTools{}, fmt.Errorf("install apktool: %w", err)
	}

	jadxPath := findJADX(toolHome)
	archivePath := filepath.Join(toolHome, "jadx-"+jadxVersion+".zip")
	if err := ensureDownloadedTool(ctx, client, fallbackClient, jadxURL, archivePath); err != nil {
		return reverseTools{}, fmt.Errorf("download jadx: %w", err)
	}
	if jadxPath == "" {
		if err := extractZIP(archivePath, toolHome); err != nil {
			return reverseTools{}, fmt.Errorf("extract jadx: %w", err)
		}
		jadxPath = findJADX(toolHome)
	}
	if jadxPath == "" {
		return reverseTools{}, fmt.Errorf("jadx executable not found under %s", toolHome)
	}
	if err := os.Chmod(jadxPath, 0o755); err != nil && runtime.GOOS != "windows" {
		return reverseTools{}, fmt.Errorf("make jadx executable: %w", err)
	}
	return reverseTools{apktoolJar: apktoolPath, jadxPath: jadxPath}, nil
}

func ensureDownloadedTool(ctx context.Context, client, fallbackClient *http.Client, rawURL, path string) error {
	expected, ok := toolSHA256(rawURL)
	if !ok {
		return fmt.Errorf("no pinned SHA-256 for tool URL: %s", rawURL)
	}
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() && info.Size() > 0 {
		if err := verifySHA256(path, expected); err == nil {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove invalid cached tool: %w", err)
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Fprintf(os.Stderr, "Downloading %s...\n", filepath.Base(path))
	download := func(downloadClient *http.Client) error {
		_, actual, downloadErr := downloadFile(ctx, downloadClient, rawURL, path, nil)
		if downloadErr != nil {
			return downloadErr
		}
		if actual != expected {
			_ = os.Remove(path)
			return fmt.Errorf("SHA-256 mismatch for %s: got %s, want %s", filepath.Base(path), actual, expected)
		}
		return nil
	}
	err = download(client)
	if err == nil || fallbackClient == nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Retrying %s with standard HTTPS...\n", filepath.Base(path))
	fallbackErr := download(fallbackClient)
	if fallbackErr != nil {
		return fmt.Errorf("%w; standard HTTPS retry failed: %v", err, fallbackErr)
	}
	return nil
}

func toolSHA256(rawURL string) (string, bool) {
	switch rawURL {
	case apktoolURL:
		return apktoolSHA256, true
	case jadxURL:
		return jadxSHA256, true
	default:
		return "", false
	}
}

func verifySHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		return fmt.Errorf("SHA-256 mismatch: got %s, want %s", actual, expected)
	}
	return nil
}

func standardToolClient(proxy string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(proxy) == "" {
		return &http.Client{Transport: transport, Timeout: 2 * time.Minute}
	}
	proxyURL, err := url.Parse(proxy)
	if err != nil || (proxyURL.Scheme != "http" && proxyURL.Scheme != "https") {
		return nil
	}
	transport.Proxy = http.ProxyURL(proxyURL)
	return &http.Client{Transport: transport, Timeout: 2 * time.Minute}
}

func findJADX(toolHome string) string {
	candidates := []string{
		filepath.Join(toolHome, "bin", "jadx"),
		filepath.Join(toolHome, "bin", "jadx.bat"),
		filepath.Join(toolHome, "jadx-"+jadxVersion, "bin", "jadx"),
		filepath.Join(toolHome, "jadx-"+jadxVersion, "bin", "jadx.bat"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func runReverseCommand(ctx context.Context, name, executable string, args ...string) error {
	var command *exec.Cmd
	if name == "apktool" {
		command = exec.CommandContext(ctx, "java", append([]string{"-jar", executable}, args...)...)
	} else {
		command = exec.CommandContext(ctx, executable, args...)
	}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return err
	}
	return nil
}

func isReverseInput(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".apk" || ext == ".apkx" || ext == ".xapk" || ext == ".apks" || ext == ".apkm"
}

func isBundleInput(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".apkx" || ext == ".xapk" || ext == ".apks" || ext == ".apkm"
}

func selectBundleAPK(archivePath, destination string) (string, error) {
	if err := extractZIP(archivePath, destination); err != nil {
		return "", fmt.Errorf("extract bundle: %w", err)
	}
	var apks []string
	err := filepath.Walk(destination, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.IsDir() && strings.EqualFold(filepath.Ext(path), ".apk") {
			apks = append(apks, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("find bundle APKs: %w", err)
	}
	if len(apks) == 0 {
		return "", fmt.Errorf("bundle contains no APK files: %s", archivePath)
	}
	sort.Strings(apks)

	if selected := metadataBaseAPK(destination, "manifest.json"); selected != "" {
		if path := bundlePath(destination, selected); path != "" {
			fmt.Fprintf(os.Stderr, "Selected APK from bundle metadata: %s\n", selected)
			return path, nil
		}
	}
	if selected := metadataBaseAPK(destination, "info.json"); selected != "" {
		if path := bundlePath(destination, selected); path != "" {
			fmt.Fprintf(os.Stderr, "Selected APK from bundle metadata: %s\n", selected)
			return path, nil
		}
	}
	for _, path := range apks {
		if strings.EqualFold(filepath.Base(path), "base.apk") {
			fmt.Fprintln(os.Stderr, "Selected APK by base.apk fallback")
			return path, nil
		}
	}
	candidates := make([]string, 0, len(apks))
	for _, path := range apks {
		if !strings.HasPrefix(strings.ToLower(filepath.Base(path)), "config") {
			candidates = append(candidates, path)
		}
	}
	if len(candidates) > 0 {
		apks = candidates
	}
	selected := apks[0]
	for _, path := range apks[1:] {
		selectedInfo, selectedErr := os.Stat(selected)
		info, statErr := os.Stat(path)
		if selectedErr == nil && statErr == nil && info.Size() > selectedInfo.Size() {
			selected = path
		}
	}
	fmt.Fprintf(os.Stderr, "Selected APK by largest size heuristic: %s\n", filepath.Base(selected))
	return selected, nil
}

func metadataBaseAPK(root, name string) string {
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return ""
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	return findBaseFile(value)
}

func findBaseFile(value any) string {
	switch item := value.(type) {
	case []any:
		for _, child := range item {
			if result := findBaseFile(child); result != "" {
				return result
			}
		}
	case map[string]any:
		id := strings.ToLower(stringValue(item, "id", "split_id", "splitId"))
		file := stringValue(item, "file", "name", "filename")
		base := boolValue(item, "base", "is_base", "isBase", "required")
		if file != "" && (id == "base" || base || strings.EqualFold(filepath.Base(file), "base.apk")) {
			return file
		}
		for _, key := range []string{"base", "base_apk", "baseApk"} {
			switch baseValue := item[key].(type) {
			case string:
				if baseValue != "" {
					return baseValue
				}
			case map[string]any:
				if fileName := stringValue(baseValue, "file", "name", "filename"); fileName != "" {
					return fileName
				}
				if result := findBaseFile(baseValue); result != "" {
					return result
				}
			}
		}
		for _, child := range item {
			if result := findBaseFile(child); result != "" {
				return result
			}
		}
	}
	return ""
}

func stringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func boolValue(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := values[key].(bool); ok && value {
			return true
		}
	}
	return false
}

func bundlePath(root, name string) string {
	name = filepath.Clean(filepath.FromSlash(name))
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return ""
	}
	path := filepath.Join(root, name)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	return path
}

func extractZIP(archivePath, destination string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	for _, file := range archive.File {
		path, err := zipEntryPath(destination, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			continue
		}
		if file.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle contains unsupported symbolic link: %s", file.Name)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		reader, err := file.Open()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			_ = reader.Close()
			return err
		}
		_, copyErr := io.Copy(output, reader)
		closeOutputErr := output.Close()
		closeReaderErr := reader.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutputErr != nil {
			return closeOutputErr
		}
		if closeReaderErr != nil {
			return closeReaderErr
		}
	}
	return nil
}

func zipEntryPath(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path in bundle: %q", name)
	}
	return filepath.Join(root, clean), nil
}
