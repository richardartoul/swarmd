package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
)

func TestSandboxBlocksExternalCommands(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("echo ok; date || true"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "ok\n"))
	qt.Assert(t, qt.Equals(strings.Contains(stderr.String(), "external commands disabled in sandbox"), true))
}

func TestSandboxRunsCoreutils(t *testing.T) {
	root := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0o644)))

	var stdout bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &bytes.Buffer{})
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("ls"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(strings.Contains(stdout.String(), "hello.txt"), true))
}

func TestSandboxCoreutilsRejectsRootedPathsOutsideSandbox(t *testing.T) {
	root := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "rooted.txt"), []byte("hello"), 0o644)))

	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("cat /rooted.txt"), "")
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), ""))
	qt.Assert(t, qt.Equals(strings.Contains(err.Error(), "permission denied"), true))
}

func TestSandboxCoreutilsMapsPWDExpandedPathsIntoSandbox(t *testing.T) {
	root := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "rooted.txt"), []byte("root"), 0o644)))
	qt.Assert(t, qt.IsNil(os.Mkdir(filepath.Join(root, "sub"), 0o755)))
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "sub", "nested.txt"), []byte("nested"), 0o644)))

	var stdout bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &bytes.Buffer{})
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader(`cat "$PWD/rooted.txt"; printf '\n'; cd sub; cat "$PWD/nested.txt"`), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "root\nnested"))
}

func TestSandboxCoreutilsRejectsWritingRootedPathsOutsideSandbox(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("mkdir /dir; touch /dir/file.txt; cp /dir/file.txt /copy.txt; mv /copy.txt /dir/moved.txt"), "")
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.Equals(strings.Contains(err.Error(), "permission denied"), true))

	_, err = os.Stat(filepath.Join(root, "dir"))
	qt.Assert(t, qt.Equals(os.IsNotExist(err), true))
	_, err = os.Stat(filepath.Join(root, "copy.txt"))
	qt.Assert(t, qt.Equals(os.IsNotExist(err), true))
}

func TestSandboxSupportsDevNullPaths(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("echo hi >/dev/null; cat </dev/null; cat /dev/null; echo ok"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "ok\n"))

	_, statErr := os.Stat(filepath.Join(root, "dev", "null"))
	qt.Assert(t, qt.Equals(os.IsNotExist(statErr), true))
}

func TestSandboxDoesNotCreateDevNullForMutations(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("touch /dev/null"), "")
	qt.Assert(t, qt.IsNotNil(err))

	_, statErr := os.Stat(filepath.Join(root, "dev", "null"))
	qt.Assert(t, qt.Equals(os.IsNotExist(statErr), true))
}

func TestSandboxCoreutilsRejectsEscapePath(t *testing.T) {
	root := t.TempDir()
	outsideRoot := filepath.Dir(root)
	target := filepath.Join(outsideRoot, "sandbox-escape.txt")
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("touch ../sandbox-escape.txt"), "")
	qt.Assert(t, qt.IsNotNil(err))
	_, statErr := os.Stat(target)
	qt.Assert(t, qt.Equals(os.IsNotExist(statErr), true))
}

func TestSandboxBlocksSymlinkEscapeViaCoreutils(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	qt.Assert(t, qt.IsNil(os.WriteFile(outsideFile, []byte("outside"), 0o644)))
	qt.Assert(t, qt.IsNil(os.Symlink(outsideFile, filepath.Join(root, "escape-link"))))

	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("cat escape-link"), "")
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), ""))
}

func TestSandboxBlocksTarHardLinkEscapeViaSymlink(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.txt")
	qt.Assert(t, qt.IsNil(os.WriteFile(outsideFile, []byte("outside"), 0o644)))
	qt.Assert(t, qt.IsNil(os.Symlink(outsideFile, filepath.Join(root, "escape-link"))))

	archivePath := filepath.Join(root, "escape.tar")
	archiveFile, err := os.Create(archivePath)
	qt.Assert(t, qt.IsNil(err))
	archiveWriter := tar.NewWriter(archiveFile)
	qt.Assert(t, qt.IsNil(archiveWriter.WriteHeader(&tar.Header{
		Name:     "linked.txt",
		Typeflag: tar.TypeLink,
		Linkname: "escape-link",
		Mode:     0o644,
	})))
	qt.Assert(t, qt.IsNil(archiveWriter.Close()))
	qt.Assert(t, qt.IsNil(archiveFile.Close()))

	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("tar -xf escape.tar ."), "")
	qt.Assert(t, qt.IsNotNil(err))
	_, statErr := os.Stat(filepath.Join(root, "linked.txt"))
	qt.Assert(t, qt.Equals(os.IsNotExist(statErr), true))
}

func TestSandboxPreventsCdEscape(t *testing.T) {
	root := t.TempDir()
	var stdout bytes.Buffer
	policy := mustSandboxFS(t, root)
	r, err := sandbox.NewRunner(policy, nil, &stdout, &bytes.Buffer{})
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("pwd; cd .. || true; pwd"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), policy.Root()+"\n"+policy.Root()+"\n"))
}

func TestSandboxAllowsTestBuiltinsWithoutEscapingRoot(t *testing.T) {
	root := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "readable.txt"), []byte("hello"), 0o644)))

	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("[ -r readable.txt ] && echo readable; test -r /etc/passwd || echo blocked"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "readable\nblocked\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))
}

func TestSandboxRejectsUnsupportedExtraFDRedirects(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("echo ok 3>&1"), "")
	qt.Assert(t, qt.Equals(err, error(interp.ExitStatus(1))))
	qt.Assert(t, qt.Equals(stdout.String(), ""))
	qt.Assert(t, qt.Equals(strings.Contains(stderr.String(), "unsupported redirect fd: 3"), true))
}

func TestSandboxCommandDiscoveryReportsVisibleCommands(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("command -v sh; command -V sh; command -v awk; command -V awk; command -v echo"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "sh\nsh is a sandbox command\nawk\nawk is a sandbox command\necho\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))
}

func TestSandboxCommandDiscoveryIncludesCustomCommandsAcrossNestedSh(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunnerWithConfig(mustSandboxFS(t, root), sandbox.RunnerConfig{
		Stdout: &stdout,
		Stderr: &stderr,
		CustomCommands: []sandbox.CustomCommand{{
			Info: sandbox.CommandInfo{
				Name:        "trace",
				Usage:       "trace <message...>",
				Description: "write a trace line to stdout",
			},
			Run: func(ctx context.Context, args []string) error {
				hc := interp.HandlerCtx(ctx)
				_, _ = fmt.Fprintln(hc.Stdout, strings.Join(args, " "))
				return nil
			},
		}},
	})
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader(`command -v trace; sh -c 'command -v trace; trace inner'; trace outer`), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "trace\ntrace\ninner\nouter\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))
}

func TestSandboxCommandDiscoveryDoesNotReportMissingCommands(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("command -v git"), "")
	qt.Assert(t, qt.Equals(err, error(interp.ExitStatus(1))))
	qt.Assert(t, qt.Equals(stdout.String(), ""))
	qt.Assert(t, qt.Equals(stderr.String(), ""))
}

func TestSandboxCommandExecUsesPolicyAwareDispatch(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader(`command awk 'BEGIN { print "ok" }'`), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "ok\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))

	stdout.Reset()
	stderr.Reset()
	err = run(r, strings.NewReader(`command sh -c 'echo ok'`), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "ok\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))

	stdout.Reset()
	stderr.Reset()
	err = run(r, strings.NewReader("echo() { printf 'shadow\\n'; }; command echo hi"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "hi\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))

	stdout.Reset()
	stderr.Reset()
	err = run(r, strings.NewReader("command eval 'echo nope'"), "")
	qt.Assert(t, qt.Equals(err, error(interp.ExitStatus(1))))
	qt.Assert(t, qt.Equals(stdout.String(), ""))
	qt.Assert(t, qt.Equals(strings.Contains(stderr.String(), "eval: builtin disabled in sandbox"), true))

	stdout.Reset()
	stderr.Reset()
	err = run(r, strings.NewReader("command -p -v ls"), "")
	qt.Assert(t, qt.Equals(err, error(interp.ExitStatus(1))))
	qt.Assert(t, qt.Equals(stdout.String(), ""))
	qt.Assert(t, qt.Equals(strings.Contains(stderr.String(), "command: builtin disabled in sandbox"), true))
}

func TestSandboxShExecutesIsolatedInlinePrograms(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader(`foo=outer; bar=outer sh -c 'printf "%s/%s\n" "${foo-unset}" "$bar"; foo=inner; bar=inner'; printf "%s/%s\n" "${foo-unset}" "${bar-unset}"`), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "unset/outer\nouter/unset\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))
}

func TestSandboxShExecutesScriptFiles(t *testing.T) {
	root := t.TempDir()
	qt.Assert(t, qt.IsNil(os.WriteFile(filepath.Join(root, "script.sh"), []byte(`printf '%s/%s/%s\n' "$0" "$1" "$PWD"`), 0o644)))

	var stdout, stderr bytes.Buffer
	policy := mustSandboxFS(t, root)
	r, err := sandbox.NewRunner(policy, nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("mkdir dir; cd dir; sh ../script.sh arg"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "../script.sh/arg/"+filepath.Join(policy.Root(), "dir")+"\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))
}

func TestSandboxAllowsLoopControlBuiltins(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), nil, &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("for i in 1 2 3; do case $i in 1) continue ;; 2) echo $i; break ;; esac; echo nope; done; echo done"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "2\ndone\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))
}

func TestSandboxXargsDispatchesBuiltins(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), strings.NewReader("hello\n"), &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("xargs echo"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "hello\n"))
}

func TestSandboxWrappedCommandsRespectBuiltinPolicy(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), strings.NewReader(""), &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("env command -v awk"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "awk\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))

	stdout.Reset()
	stderr.Reset()
	err = run(r, strings.NewReader("env eval 'echo nope'"), "")
	qt.Assert(t, qt.Equals(err, error(interp.ExitStatus(1))))
	qt.Assert(t, qt.Equals(stdout.String(), ""))
	qt.Assert(t, qt.Equals(strings.Contains(stderr.String(), "eval: builtin disabled in sandbox"), true))

	stdout.Reset()
	stderr.Reset()
	err = run(r, strings.NewReader("xargs command -v awk"), "")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stdout.String(), "awk\n"))
	qt.Assert(t, qt.Equals(stderr.String(), ""))

	stdout.Reset()
	stderr.Reset()
	err = run(r, strings.NewReader("xargs exec true"), "")
	qt.Assert(t, qt.Equals(err, error(interp.ExitStatus(1))))
	qt.Assert(t, qt.Equals(stdout.String(), ""))
	qt.Assert(t, qt.Equals(strings.Contains(stderr.String(), "exec: builtin disabled in sandbox"), true))
}

func TestSandboxBlocksExternalExecViaXargs(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	r, err := sandbox.NewRunner(mustSandboxFS(t, root), strings.NewReader("hello\n"), &stdout, &stderr)
	qt.Assert(t, qt.IsNil(err))

	err = run(r, strings.NewReader("xargs date"), "")
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.Equals(strings.Contains(stderr.String(), "external commands disabled in sandbox"), true))
}

func mustSandboxFS(t *testing.T, root string) *sandbox.FS {
	t.Helper()
	policy, err := sandbox.NewFS(root)
	qt.Assert(t, qt.IsNil(err))
	return policy
}
