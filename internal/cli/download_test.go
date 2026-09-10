package cli

import (
	"archive/zip"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucasew/revancedbot/internal/download"
)

func TestDownload_cacheHitIdentityOK(t *testing.T) {
	resetCLI(t)
	t.Setenv("CI", "1")
	orig := checkStockIdentity
	checkStockIdentity = func(string, string, string) error { return nil }
	t.Cleanup(func() { checkStockIdentity = orig })

	repo := writeRepoYAML(t, "repo_name: t\n")
	cache := t.TempDir()
	stock := filepath.Join(cache, "stock")
	if err := os.MkdirAll(stock, 0o755); err != nil {
		t.Fatal(err)
	}
	apk := filepath.Join(stock, "com.example.app_latest.apk")
	if err := writeMinimalAPK(apk); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := captureStd(t, func() error {
		root := NewRoot()
		root.SetArgs([]string{"download", repo, "--package", "com.example.app", "--cache", cache})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	line := strings.TrimSpace(stdout)
	want := "cache\t" + apk
	if line != want {
		t.Fatalf("stdout = %q; want %q", line, want)
	}
}

func TestDownload_cacheHitIdentityRejectDoesNotPrintCache(t *testing.T) {
	resetCLI(t)
	t.Setenv("CI", "1")
	orig := checkStockIdentity
	checkStockIdentity = func(string, string, string) error {
		return fmt.Errorf("package mismatch: %w", download.ErrBase)
	}
	t.Cleanup(func() { checkStockIdentity = orig })

	repo := writeRepoYAML(t, "repo_name: t\ndownloaders: [nosuch]\n")
	cache := t.TempDir()
	stock := filepath.Join(cache, "stock")
	if err := os.MkdirAll(stock, 0o755); err != nil {
		t.Fatal(err)
	}
	apk := filepath.Join(stock, "com.example.app_latest.apk")
	if err := writeMinimalAPK(apk); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := captureStd(t, func() error {
		root := NewRoot()
		root.SetArgs([]string{"download", repo, "--package", "com.example.app", "--cache", cache})
		return root.Execute()
	})
	if err == nil {
		t.Fatal("expected identity reject to fail the command")
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "cache\t") {
		t.Fatalf("stdout leaked cache hit: %q", stdout)
	}
	if _, statErr := os.Stat(apk); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatal("rejected cache APK must be deleted")
	}
}

func writeMinimalAPK(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "AndroidManifest.xml", Method: zip.Store}
	w, err := zw.CreateHeader(h)
	if err != nil {
		if cErr := zw.Close(); cErr != nil {
			return errors.Join(err, cErr)
		}
		return err
	}
	if _, err := w.Write(make([]byte, int(download.MinAPKBytes)+64)); err != nil {
		if cErr := zw.Close(); cErr != nil {
			return errors.Join(err, cErr)
		}
		return err
	}
	return zw.Close()
}
