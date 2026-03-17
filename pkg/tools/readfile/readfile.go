package readfile

import (
	"context"
	"fmt"
	"strings"
	"sync"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

const (
	toolName              = "read_file"
	defaultReadFileLimit  = 200
	maxReadFileLimit      = 1200
	maxReadFileBytes      = 4 << 20
	defaultReadBlockLines = 240
)

var registerOnce sync.Once

type args struct {
	FilePath    string             `json:"file_path"`
	Offset      int                `json:"offset"`
	Limit       int                `json:"limit"`
	Mode        string             `json:"mode"`
	Indentation *indentationConfig `json:"indentation"`
}

type indentationConfig struct {
	AnchorLine      int  `json:"anchor_line"`
	MaxLevels       int  `json:"max_levels"`
	IncludeSiblings bool `json:"include_siblings"`
	IncludeHeader   bool `json:"include_header"`
	MaxLines        int  `json:"max_lines"`
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
		Description: "Reads a local file with 1-indexed line numbers and bounded output.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"file_path": toolscommon.StringSchema("Absolute path to the file."),
				"offset":    toolscommon.NumberSchema("The 1-indexed line number to start reading from."),
				"limit":     toolscommon.NumberSchema("The maximum number of lines to return."),
				"mode":      toolscommon.StringEnumSchema(`Optional mode selector. Use "slice" (default) or "indentation".`, "slice", "indentation"),
				"indentation": toolscommon.ObjectSchema(
					map[string]any{
						"anchor_line":      toolscommon.NumberSchema("Anchor line to center the indentation lookup on."),
						"max_levels":       toolscommon.NumberSchema("How many parent indentation levels to include."),
						"include_siblings": toolscommon.BooleanSchema("When true, include additional blocks that share the anchor indentation."),
						"include_header":   toolscommon.BooleanSchema("Include doc comments or attributes directly above the selected block."),
						"max_lines":        toolscommon.NumberSchema("Hard cap on the number of lines returned in indentation mode."),
					},
				),
			},
			"file_path",
		),
		RequiredArguments: []string{"file_path"},
		Examples: []string{
			`{"file_path":"/workspace/main.go","offset":1,"limit":120}`,
			`{"file_path":"/workspace/main.go","mode":"indentation","indentation":{"anchor_line":80,"include_header":true,"max_lines":80}}`,
		},
		OutputNotes: "Returns a numbered line-oriented slice with a file header and range summary.",
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
	if err := validateArgs(args); err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	filePath := strings.TrimSpace(args.FilePath)
	if filePath == "" {
		toolCtx.SetPolicyError(step, fmt.Errorf("file_path must not be empty"))
		return nil
	}
	resolved, err := toolCtx.ResolvePath(filePath)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	info, err := toolCtx.FileSystem().Stat(resolved)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if info.IsDir() {
		toolCtx.SetPolicyError(step, fmt.Errorf("%q is a directory", resolved))
		return nil
	}
	text, err := toolscommon.ReadTextFileLimited(toolCtx.FileSystem(), resolved, maxReadFileBytes)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if strings.ContainsRune(text, '\x00') {
		toolCtx.SetPolicyError(step, fmt.Errorf("%q appears to be a binary file", resolved))
		return nil
	}
	lines := toolscommon.SplitPreservingLineStructure(text)
	if len(lines) == 0 {
		toolCtx.SetOutput(step, fmt.Sprintf("File: %s\nFile is empty.\n", resolved))
		return nil
	}

	start, end, ok := selectReadFileRange(lines, args)
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s\n", resolved)
	if !ok {
		b.WriteString("No lines in this range.\n")
		toolCtx.SetOutput(step, b.String())
		return nil
	}
	fmt.Fprintf(&b, "Showing lines %d-%d of %d\n", start+1, end, len(lines))
	toolscommon.RenderNumberedLines(&b, lines[start:end], start+1)
	toolCtx.SetOutput(step, b.String())
	return nil
}

func validateArgs(args args) error {
	switch normalizeMode(args.Mode) {
	case "slice":
		if args.Indentation != nil {
			return fmt.Errorf(`indentation requires mode "indentation"`)
		}
	case "indentation":
		return nil
	default:
		return fmt.Errorf(`mode must be "slice" or "indentation"`)
	}
	return nil
}

func normalizeMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return "slice"
	}
	return mode
}

func selectReadFileRange(lines []string, args args) (int, int, bool) {
	if normalizeMode(args.Mode) == "indentation" {
		start, end := selectIndentedRange(lines, args)
		return start, end, true
	}
	return toolscommon.NormalizePaginationWindow(args.Offset, args.Limit, defaultReadFileLimit, maxReadFileLimit).RangeForTotal(len(lines))
}

func selectIndentedRange(lines []string, args args) (int, int) {
	indentation := args.Indentation
	if indentation == nil {
		indentation = &indentationConfig{}
	}
	anchorLine := indentation.AnchorLine
	if anchorLine <= 0 {
		anchorLine = args.Offset
	}
	if anchorLine <= 0 {
		anchorLine = 1
	}
	anchorIdx := toolscommon.MinInt(toolscommon.MaxInt(anchorLine-1, 0), len(lines)-1)
	anchorIndent := lineIndent(lines[anchorIdx])
	start := anchorIdx
	for start > 0 {
		prev := lines[start-1]
		if strings.TrimSpace(prev) == "" {
			break
		}
		if lineIndent(prev) < anchorIndent {
			break
		}
		start--
	}
	end := anchorIdx + 1
	for end < len(lines) {
		line := lines[end]
		if strings.TrimSpace(line) == "" {
			end++
			continue
		}
		if lineIndent(line) < anchorIndent {
			break
		}
		end++
	}
	for levels, currentIndent, idx := 0, anchorIndent, start-1; levels < indentation.MaxLevels && idx >= 0; idx-- {
		if strings.TrimSpace(lines[idx]) == "" {
			continue
		}
		indent := lineIndent(lines[idx])
		if indent < currentIndent {
			start = idx
			currentIndent = indent
			levels++
		}
	}
	if indentation.IncludeSiblings {
		for end < len(lines) {
			next := nextNonBlankLine(lines, end)
			if next < 0 || lineIndent(lines[next]) != anchorIndent {
				break
			}
			siblingEnd := next + 1
			for siblingEnd < len(lines) {
				line := lines[siblingEnd]
				if strings.TrimSpace(line) == "" {
					siblingEnd++
					continue
				}
				if lineIndent(line) < anchorIndent {
					break
				}
				siblingEnd++
			}
			end = siblingEnd
		}
	}
	if indentation.IncludeHeader {
		for start > 0 {
			line := strings.TrimSpace(lines[start-1])
			if line == "" {
				start--
				continue
			}
			if !isHeaderLikeLine(line) {
				break
			}
			start--
		}
	}
	maxLines := toolscommon.ClampInt(indentation.MaxLines, defaultReadBlockLines, maxReadFileLimit)
	if end-start > maxLines {
		end = start + maxLines
	}
	return start, end
}

func nextNonBlankLine(lines []string, start int) int {
	for idx := start; idx < len(lines); idx++ {
		if strings.TrimSpace(lines[idx]) != "" {
			return idx
		}
	}
	return -1
}

func lineIndent(line string) int {
	width := 0
	for _, r := range line {
		switch r {
		case ' ':
			width++
		case '\t':
			width += 4
		default:
			return width
		}
	}
	return width
}

func isHeaderLikeLine(line string) bool {
	return strings.HasPrefix(line, "//") ||
		strings.HasPrefix(line, "#") ||
		strings.HasPrefix(line, "/*") ||
		strings.HasPrefix(line, "*") ||
		strings.HasPrefix(line, "@") ||
		(strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"))
}
