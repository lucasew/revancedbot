package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lucasew/revancedbot/internal/config"
)

func TestRun_missingAuthorityDoc(t *testing.T) {
	resetCLI(t)
	t.Setenv("CI", "1")
	repo := t.TempDir()
	root := NewRoot()
	root.SetArgs([]string{"run", repo})
	err := root.Execute()
	if err == nil {
		t.Fatal("run: missing REPO/revancedbot.yaml and no --config must error")
	}
	if !errors.Is(err, config.ErrMissingAuthorityDoc) {
		t.Fatalf("run: want ErrMissingAuthorityDoc, got %v", err)
	}
}

func TestKeysGenerate_stdoutOneLine(t *testing.T) {
	resetCLI(t)
	t.Setenv("CI", "1")

	const blob = "PASTEABLEBLOB"
	origCheck, origGen := checkKeyTools, generateKeys
	checkKeyTools = func() error { return nil }
	generateKeys = func(alias string) (string, error) {
		if alias != "" {
			t.Errorf("alias = %q; want empty", alias)
		}
		return blob, nil
	}
	t.Cleanup(func() {
		checkKeyTools, generateKeys = origCheck, origGen
	})

	stdout, stderr, err := captureStd(t, func() error {
		root := NewRoot()
		root.SetArgs([]string{"keys", "generate"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("keys generate: %v\nstderr=%s", err, stderr)
	}
	line, extra := splitOneLine(stdout)
	if extra || line == "" {
		t.Fatalf("stdout want exactly one non-empty line, got %q", stdout)
	}
	if line != blob {
		t.Fatalf("stdout = %q; want %q", line, blob)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Fatal("expected logs on stderr")
	}
	if strings.Contains(stderr, blob) {
		t.Fatalf("blob leaked onto stderr: %q", stderr)
	}
}

func TestKeysGenerate_rejectsAlias(t *testing.T) {
	cmd := newKeysGenerateCmd()
	if cmd.Flags().Lookup("alias") != nil {
		t.Fatal("keys generate must not define --alias")
	}
}

func resetCLI(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		cfgFile = ""
		cacheFlag = ""
		if err := CloseSession(); err != nil {
			t.Errorf("CloseSession: %v", err)
		}
	})
}

func captureStd(t *testing.T, fn func() error) (stdout, stderr string, err error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	rErr, wErr, e := os.Pipe()
	if e != nil {
		t.Fatal(e)
	}
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	var outBuf, errBuf bytes.Buffer
	outDone, errDone := make(chan struct{}), make(chan struct{})
	go func() {
		if _, copyErr := io.Copy(&outBuf, rOut); copyErr != nil {
			t.Errorf("stdout copy: %v", copyErr)
		}
		close(outDone)
	}()
	go func() {
		if _, copyErr := io.Copy(&errBuf, rErr); copyErr != nil {
			t.Errorf("stderr copy: %v", copyErr)
		}
		close(errDone)
	}()
	err = fn()
	if cerr := wOut.Close(); cerr != nil {
		t.Errorf("stdout close: %v", cerr)
	}
	if cerr := wErr.Close(); cerr != nil {
		t.Errorf("stderr close: %v", cerr)
	}
	<-outDone
	<-errDone
	return outBuf.String(), errBuf.String(), err
}

func splitOneLine(s string) (line string, extra bool) {
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	if strings.ContainsAny(s, "\n\r") {
		return s, true
	}
	return s, false
}
