// See LICENSE for licensing information

package sandbox_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	"github.com/richardartoul/swarmd/pkg/sh/syntax"
)

func TestSandboxRunnerSupportsPosixFindOrdering(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "subdir", "hello.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	policy, err := sandbox.NewFS(root)
	if err != nil {
		t.Fatalf("sandbox.NewFS error: %v", err)
	}
	runner, err := sandbox.NewRunner(policy, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("sandbox.NewRunner error: %v", err)
	}

	parser := syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
	program, err := parser.Parse(strings.NewReader("find . -type f"), "")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if err := runner.Run(context.Background(), program); err != nil {
		t.Fatalf("Runner.Run error: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "subdir/hello.txt") {
		t.Fatalf("stdout = %q, want file path", stdout.String())
	}
}

func TestSandboxRunnerExpandsGlobsByDefault(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "two.md"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	policy, err := sandbox.NewFS(root)
	if err != nil {
		t.Fatalf("sandbox.NewFS error: %v", err)
	}
	runner, err := sandbox.NewRunner(policy, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("sandbox.NewRunner error: %v", err)
	}

	parser := syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
	program, err := parser.Parse(strings.NewReader(`for arg in *.txt *.nomatch *; do printf '<%s>\n' "$arg"; done`), "")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if err := runner.Run(context.Background(), program); err != nil {
		t.Fatalf("Runner.Run error: %v\nstderr=%s", err, stderr.String())
	}
	if got, want := stdout.String(), "<one.txt>\n<*.nomatch>\n<one.txt>\n<two.md>\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSandboxRunnerCanDisableGlobbing(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	policy, err := sandbox.NewFS(root)
	if err != nil {
		t.Fatalf("sandbox.NewFS error: %v", err)
	}
	runner, err := sandbox.NewRunnerWithConfig(policy, sandbox.RunnerConfig{
		Stdout:          &stdout,
		Stderr:          &stderr,
		DisableGlobbing: true,
	})
	if err != nil {
		t.Fatalf("sandbox.NewRunnerWithConfig error: %v", err)
	}

	parser := syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
	program, err := parser.Parse(strings.NewReader(`for arg in *.txt *.nomatch *; do printf '<%s>\n' "$arg"; done`), "")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if err := runner.Run(context.Background(), program); err != nil {
		t.Fatalf("Runner.Run error: %v\nstderr=%s", err, stderr.String())
	}
	if got, want := stdout.String(), "<*.txt>\n<*.nomatch>\n<*>\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSandboxRunnerHidesBashOnlyBuiltinsInPosixMode(t *testing.T) {
	root := t.TempDir()

	var stdout, stderr bytes.Buffer
	policy, err := sandbox.NewFS(root)
	if err != nil {
		t.Fatalf("sandbox.NewFS error: %v", err)
	}
	runner, err := sandbox.NewRunner(policy, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("sandbox.NewRunner error: %v", err)
	}

	parser := syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
	program, err := parser.Parse(strings.NewReader("source missing.sh"), "")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	err = runner.Run(context.Background(), program)
	if got, want := err, interp.ExitStatus(126); got != want {
		t.Fatalf("Runner.Run error = %v, want %v", got, want)
	}
	if !strings.Contains(stderr.String(), "source: external commands disabled in sandbox") {
		t.Fatalf("stderr = %q, want external-command failure", stderr.String())
	}
}
