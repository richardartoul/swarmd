// Package tooltest provides a sandbox-backed [toolscore.ToolContext] for
// tool-handler tests: a real root-constrained filesystem in a temp directory,
// step-recording outcome setters, and an optional HTTP transport.
package tooltest

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

// Context is a minimal ToolContext for exercising tool handlers. Unset
// capabilities fall back to [toolscore.UnimplementedToolContext].
type Context struct {
	toolscore.UnimplementedToolContext

	// FS is the sandbox filesystem rooted at Root.
	FS *sandbox.FS
	// Root is the resolved sandbox root directory.
	Root string
	// Transport, when set, backs the HTTPClient capability.
	Transport http.RoundTripper
}

// NewContext returns a Context rooted at a fresh test temp directory.
func NewContext(t *testing.T) *Context {
	t.Helper()
	root := t.TempDir()
	fs, err := sandbox.NewFS(root)
	if err != nil {
		t.Fatalf("sandbox.NewFS() error = %v", err)
	}
	resolvedRoot, err := fs.Getwd()
	if err != nil {
		t.Fatalf("fs.Getwd() error = %v", err)
	}
	return &Context{FS: fs, Root: resolvedRoot}
}

func (c *Context) WorkingDir() string             { return c.Root }
func (c *Context) FileSystem() sandbox.FileSystem { return c.FS }

func (c *Context) ResolvePath(path string) (string, error) {
	return sandbox.ResolvePath(c.FS, c.Root, path)
}

func (c *Context) HTTPClient(opts toolscore.ToolHTTPClientOptions) *http.Client {
	if c.Transport == nil {
		return nil
	}
	client := &http.Client{Transport: c.Transport}
	if !opts.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return client
}

// WriteFile writes a file (creating parent directories) under the sandbox
// root and returns its resolved path.
func (c *Context) WriteFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(c.Root, name)
	if err := c.FS.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := c.FS.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

// ReadFile reads a file under the sandbox root from the host filesystem.
func (c *Context) ReadFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.Root, name))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", name, err)
	}
	return string(data)
}

// Invoke runs one tool handler with the given JSON (or custom-format) input
// and returns the recorded step.
func Invoke(t *testing.T, toolCtx toolscore.ToolContext, plugin toolscore.ToolPlugin, input string) toolscore.Step {
	t.Helper()
	definition := plugin.Definition()
	handler, err := plugin.NewHandler(toolscore.ConfiguredTool{ID: definition.Name})
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	step := toolscore.Step{}
	err = handler.Invoke(context.Background(), toolCtx, &step, &toolscore.ToolAction{
		Name:  definition.Name,
		Kind:  definition.Kind,
		Input: input,
	})
	if err != nil {
		t.Fatalf("Invoke() error = %v", err)
	}
	return step
}
