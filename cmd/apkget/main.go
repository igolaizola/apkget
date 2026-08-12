package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/igolaizola/apkget"
)

var version = ""
var commit = ""
var date = ""

func main() {
	// Cancel network requests cleanly when the user presses Ctrl-C.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage()
		return flag.ErrHelp
	}
	switch args[0] {
	case "version":
		v := version
		if v == "" {
			v = "dev"
		}
		parts := []string{v}
		if commit != "" {
			parts = append(parts, commit)
		}
		if date != "" {
			parts = append(parts, date)
		}
		fmt.Println(strings.Join(parts, " "))
		return nil
	case "download":
		return download(ctx, args[0], args[1:])
	case "list":
		return listVersions(ctx, args[1:])
	case "logo":
		return logo(ctx, args[1:])
	case "sources":
		for _, name := range apkget.NewDownloader(nil, nil).Sources() {
			fmt.Println(name)
		}
		return nil
	default:
		// Download is the primary command, so an app query may be supplied
		// directly without spelling out a subcommand.
		return download(ctx, "download", args)
	}
}

func download(ctx context.Context, command string, args []string) error {
	flagName := command
	usageName := "apkget " + command
	if command == "download" {
		flagName = "apkget"
		usageName = "apkget"
	}
	fs := flag.NewFlagSet(flagName, flag.ContinueOnError)
	out := fs.String("output", ".", "output directory or file path")
	source := fs.String("source", "", "source to use (default: automatic fallback)")
	versionFlag := fs.String("version", "", "exact version")
	proxy := fs.String("proxy", "", "HTTP/SOCKS proxy URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: " + usageName + " [flags] <app-name|package-id>")
	}
	query := fs.Arg(0)
	client := apkget.NewHTTPClient(*proxy)
	// Resolve names before creating the output staging directory; failed
	// searches should not leave any filesystem artifacts behind.
	packageID, err := apkget.ResolvePackageID(ctx, query, client)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Found package: %s\n", packageID)
	outputPath, fileOutput, err := outputTarget(*out)
	if err != nil {
		return err
	}
	outputDir := *out
	var temporaryDir string
	if fileOutput {
		// The library writes to a directory. Stage there, then move the final
		// artifact to the exact filename requested by the CLI user.
		temporaryDir, err = os.MkdirTemp("", "apkget-download-*")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(temporaryDir) }()
		outputDir = temporaryDir
	}
	result, err := apkget.NewDownloader(client, nil).Download(ctx, packageID, apkget.Options{
		OutputDir: outputDir, Source: *source, Version: *versionFlag,
		Proxy: *proxy, Client: client,
	})
	if err != nil {
		return err
	}
	if fileOutput {
		// Keep the final move atomic from the caller's perspective and preserve
		// the path printed by the command.
		if err := moveFile(result.Path, outputPath); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		result.Path = outputPath
	}
	fmt.Println(result.Path)
	return nil
}

func outputTarget(path string) (string, bool, error) {
	if path == "" {
		path = "."
	}
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return path, false, nil
		}
		return path, true, nil
	}
	if !os.IsNotExist(err) {
		return "", false, err
	}
	// A missing path is treated as a directory unless it looks like a file.
	// This lets users write either `-output ./downloads` or `-output ./app.apk`.
	if strings.HasSuffix(path, "/") || strings.HasSuffix(path, string(filepath.Separator)) || filepath.Ext(path) == "" {
		return path, false, nil
	}
	return path, true, nil
}

func moveFile(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	part := target + ".part"
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = in.Close()
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeOutErr := out.Close()
	closeInErr := in.Close()
	if copyErr != nil {
		_ = os.Remove(part)
		return copyErr
	}
	if closeOutErr != nil {
		_ = os.Remove(part)
		return closeOutErr
	}
	if closeInErr != nil {
		_ = os.Remove(part)
		return closeInErr
	}
	if err := os.Rename(part, target); err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Remove(source)
}

func listVersions(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	source := fs.String("source", "", "source to query")
	proxy := fs.String("proxy", "", "HTTP/SOCKS proxy URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: apkget list [flags] <app-name|package-id>")
	}
	client := apkget.NewHTTPClient(*proxy)
	packageID, err := apkget.ResolvePackageID(ctx, fs.Arg(0), client)
	if err != nil {
		return err
	}
	versions, err := apkget.NewDownloader(client, nil).ListVersions(ctx, packageID, *source)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(versions)
}

func logo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("logo", flag.ContinueOnError)
	out := fs.String("output", ".", "output directory or image file")
	proxy := fs.String("proxy", "", "HTTP/SOCKS proxy URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: apkget logo [flags] <app-name|package-id>")
	}
	path, err := apkget.DownloadLogo(ctx, fs.Arg(0), *out, apkget.NewHTTPClient(*proxy))
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `apkget downloads Android packages from public APK sources.

Usage:
  apkget [flags] <app-name|package-id>
  apkget list [flags] <app-name|package-id>
  apkget logo [flags] <app-name|package-id>
  apkget sources

The app name is resolved to a package ID using Google Play search. A package ID
is passed directly, then the downloader tries sources in fallback order.`)
}
