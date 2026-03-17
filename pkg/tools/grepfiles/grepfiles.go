package grepfiles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	shexpand "github.com/richardartoul/swarmd/pkg/sh/expand"
	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	shsyntax "github.com/richardartoul/swarmd/pkg/sh/syntax"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

const (
	toolName             = "grep_files"
	defaultGrepLimit     = 100
	maxGrepLimit         = 500
	maxGrepScanFiles     = 4000
	defaultReadFileBytes = 1 << 20
)

var (
	registerOnce sync.Once
	errStopWalk  = errors.New("stop tool walk")
)

type args struct {
	Pattern string `json:"pattern"`
	Include string `json:"include"`
	Path    string `json:"path"`
	Limit   int    `json:"limit"`
}

type grepMatch struct {
	Path    string
	ModTime time.Time
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
		Description: "Searches local files for a regular expression and returns matching paths.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"pattern": toolscommon.StringSchema("Regular expression pattern to search for."),
				"include": toolscommon.StringSchema("Optional glob that limits which files are searched. Supports brace alternation like `*.{go,md}`; each expanded pattern still uses Go filepath.Match semantics."),
				"path":    toolscommon.StringSchema("Directory or file path to search. Defaults to the current working directory."),
				"limit":   toolscommon.NumberSchema("Maximum number of matching file paths to return."),
			},
			"pattern",
		),
		RequiredArguments: []string{"pattern"},
		Examples: []string{
			`{"pattern":"HandleTrigger","path":"/workspace/pkg/agent","limit":20}`,
			`{"pattern":"TODO","path":"/workspace","include":"*.{go,md}"}`,
		},
		OutputNotes: "Returns matching file paths as `index|modified_at|path` without inline content.",
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
	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("pattern must not be empty"))
		return nil
	}
	rootPath := strings.TrimSpace(args.Path)
	if rootPath == "" {
		rootPath = toolCtx.WorkingDir()
	}
	resolvedRoot, err := toolCtx.ResolvePath(rootPath)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		toolCtx.SetPolicyError(step, fmt.Errorf("invalid pattern: %w", err))
		return nil
	}
	limit := toolscommon.ClampInt(args.Limit, defaultGrepLimit, maxGrepLimit)

	var matches []grepMatch
	scanned := 0
	walkErr := walkToolFiles(toolCtx.FileSystem(), resolvedRoot, func(path string, info fs.FileInfo) error {
		if scanned >= maxGrepScanFiles || len(matches) >= limit {
			return errStopWalk
		}
		scanned++
		if !matchesInclude(args.Include, resolvedRoot, path) {
			return nil
		}
		text, err := toolscommon.ReadTextFileLimited(toolCtx.FileSystem(), path, defaultReadFileBytes)
		if err != nil {
			return nil
		}
		if strings.ContainsRune(text, '\x00') {
			return nil
		}
		if re.MatchString(text) {
			matches = append(matches, grepMatch{Path: path, ModTime: info.ModTime()})
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errStopWalk) {
		return walkErr
	}

	slices.SortFunc(matches, func(left, right grepMatch) int {
		switch {
		case left.ModTime.After(right.ModTime):
			return -1
		case left.ModTime.Before(right.ModTime):
			return 1
		default:
			return strings.Compare(left.Path, right.Path)
		}
	})

	var b strings.Builder
	fmt.Fprintf(&b, "Search root: %s\n", resolvedRoot)
	fmt.Fprintf(&b, "Pattern: %s\n", pattern)
	if len(matches) == 0 {
		b.WriteString("No matching files found.\n")
		toolCtx.SetOutput(step, b.String())
		return nil
	}
	for idx, match := range matches {
		fmt.Fprintf(&b, "%d|%s|%s\n", idx+1, match.ModTime.UTC().Format(time.RFC3339), filepath.ToSlash(match.Path))
	}
	toolCtx.SetOutput(step, b.String())
	return nil
}

func walkToolFiles(fs sandbox.FileSystem, root string, visit func(path string, info fs.FileInfo) error) error {
	info, err := fs.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return nil
		}
		return visit(root, info)
	}
	return walkToolDirectory(fs, root, visit)
}

func walkToolDirectory(fs sandbox.FileSystem, root string, visit func(path string, info fs.FileInfo) error) error {
	entries, err := fs.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		childPath := filepath.Join(root, entry.Name())
		info, err := fs.Lstat(childPath)
		if err != nil {
			continue
		}
		if info.IsDir() {
			if err := walkToolDirectory(fs, childPath, visit); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := visit(childPath, info); err != nil {
			return err
		}
	}
	return nil
}

func matchesInclude(include, root, path string) bool {
	include = strings.TrimSpace(include)
	if include == "" {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(rel)
	for _, pattern := range expandIncludePatterns(filepath.ToSlash(include)) {
		matched, err := filepath.Match(pattern, rel)
		if err == nil && matched {
			return true
		}
		baseMatched, err := filepath.Match(pattern, filepath.Base(rel))
		if err == nil && baseMatched {
			return true
		}
	}
	return false
}

func expandIncludePatterns(include string) []string {
	word := shsyntax.Word{
		Parts: []shsyntax.WordPart{&shsyntax.Lit{Value: include}},
	}
	if !shsyntax.SplitBraces(&word) {
		return []string{include}
	}
	expanded := shexpand.Braces(&word)
	patterns := make([]string, 0, len(expanded))
	seen := make(map[string]struct{}, len(expanded))
	for _, expandedWord := range expanded {
		pattern := expandedWord.Lit()
		if pattern == "" {
			continue
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		patterns = append(patterns, pattern)
	}
	if len(patterns) == 0 {
		return []string{include}
	}
	return patterns
}
