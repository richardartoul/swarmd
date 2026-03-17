package listdir

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

const (
	toolName            = "list_dir"
	defaultListDirLimit = 200
	maxListDirLimit     = 1000
	defaultListDirDepth = 1
	maxListDirDepth     = 8
)

var registerOnce sync.Once

type args struct {
	DirPath string `json:"dir_path"`
	Offset  int    `json:"offset"`
	Limit   int    `json:"limit"`
	Depth   int    `json:"depth"`
}

type listedEntry struct {
	Path string
	Kind string
}

type directoryPageCollector struct {
	window  toolscommon.PaginationWindow
	total   int
	entries []listedEntry
}

type plugin struct{}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		toolregistry.MustRegister(plugin{}, toolregistry.RegistrationOptions{BuiltIn: true})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        toolName,
		Description: "Lists entries in a local directory with bounded, typed output.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"dir_path": toolscommon.StringSchema("Absolute path to the directory to list."),
				"offset":   toolscommon.NumberSchema("The 1-indexed entry number to start listing from."),
				"limit":    toolscommon.NumberSchema("The maximum number of entries to return."),
				"depth":    toolscommon.NumberSchema("The maximum directory depth to traverse."),
			},
			"dir_path",
		),
		RequiredArguments: []string{"dir_path"},
		Examples: []string{
			`{"dir_path":"/workspace","limit":50,"depth":2}`,
		},
		OutputNotes: "Returns numbered entries as `index|kind|path` with a summary header.",
		Interop: toolscommon.ToolInterop(
			toolName,
			toolscore.ToolBoundaryKindFunction,
			toolscore.ToolBoundaryKindFunction,
			toolName,
		),
		SafetyTags: []string{"read_only"},
		ReadOnly:   true,
	}
}

func (plugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	if err := toolscommon.ValidateNoToolConfig(toolName, config.Config); err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(handle), nil
}

func handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	_ = ctx
	args, err := toolscore.DecodeToolInput[args](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	dirPath := strings.TrimSpace(args.DirPath)
	if dirPath == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("dir_path must not be empty"))
		return nil
	}
	resolved, err := toolCtx.ResolvePath(dirPath)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	info, err := toolCtx.FileSystem().Stat(resolved)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if !info.IsDir() {
		toolCtx.SetPolicyError(step, fmt.Errorf("%q is not a directory", resolved))
		return nil
	}

	window := toolscommon.NormalizePaginationWindow(args.Offset, args.Limit, defaultListDirLimit, maxListDirLimit)
	depth := toolscommon.ClampInt(args.Depth, defaultListDirDepth, maxListDirDepth)
	collector := directoryPageCollector{
		window:  window,
		entries: make([]listedEntry, 0, window.Limit),
	}
	if err := collectDirectoryEntries(toolCtx.FileSystem(), resolved, "", depth, &collector); err != nil {
		return err
	}
	if collector.total == 0 {
		toolCtx.SetOutput(step, fmt.Sprintf("Directory: %s\nNo entries found.\n", resolved))
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Directory: %s\n", resolved)
	start, end, ok := window.RangeForTotal(collector.total)
	if !ok {
		b.WriteString("No entries in this range.\n")
		toolCtx.SetOutput(step, b.String())
		return nil
	}
	fmt.Fprintf(&b, "Showing entries %d-%d of %d\n", start+1, end, collector.total)
	for idx, entry := range collector.entries {
		fmt.Fprintf(&b, "%d|%s|%s\n", start+idx+1, entry.Kind, filepath.ToSlash(entry.Path))
	}
	toolCtx.SetOutput(step, b.String())
	return nil
}

func collectDirectoryEntries(fs sandbox.FileSystem, root, rel string, depth int, collector *directoryPageCollector) error {
	if depth <= 0 {
		return nil
	}
	dirEntries, err := fs.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range dirEntries {
		childName := entry.Name()
		displayName := childName
		if rel != "" {
			displayName = filepath.Join(rel, childName)
		}
		childPath := filepath.Join(root, childName)
		collector.append(listedEntry{
			Path: displayName,
			Kind: dirEntryKind(entry),
		})
		if entry.IsDir() && depth > 1 {
			if err := collectDirectoryEntries(fs, childPath, displayName, depth-1, collector); err != nil {
				return err
			}
		}
	}
	return nil
}

func dirEntryKind(entry fs.DirEntry) string {
	switch {
	case entry.IsDir():
		return "dir"
	case entry.Type()&os.ModeSymlink != 0:
		return "symlink"
	default:
		return "file"
	}
}

func (c *directoryPageCollector) append(entry listedEntry) {
	idx := c.total
	if idx >= c.window.Start && len(c.entries) < c.window.Limit {
		c.entries = append(c.entries, entry)
	}
	c.total++
}
