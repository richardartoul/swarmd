package applypatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

const (
	toolName               = "apply_patch"
	defaultApplyPatchPerm  = 0o644
	maxApplyPatchFileBytes = 4 << 20
)

const grammar = `start: begin_patch hunk end_patch

begin_patch: "*** Begin Patch" LF
end_patch: "*** End Patch" LF?

hunk: add_hunk | update_hunk
add_hunk: "*** Add File: " filename LF add_line+
update_hunk: "*** Update File: " filename LF change?

filename: /(.+)/
add_line: "+" /(.*)/ LF -> line

change: (change_context | change_line)+ eof_line?

change_context: ("@@" | "@@ " /(.+)/) LF
change_line: ("+" | "-" | " ") /(.*)/ LF
eof_line: "*** End of File" LF

%import common.LF`

var registerOnce sync.Once

type parsedPatch struct {
	Ops []patchFileOp
}

type patchFileOp struct {
	Action   string
	Path     string
	AddLines []string
	Hunks    []patchHunk
}

type patchHunk struct {
	Header string
	Lines  []patchChangeLine
}

type patchChangeLine struct {
	Kind byte
	Text string
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
		Description: "Apply a structured patch to local files.",
		Kind:        toolscore.ToolKindCustom,
		CustomFormat: &toolscore.ToolFormat{
			Type:       "grammar",
			Syntax:     "lark",
			Definition: grammar,
		},
		Examples: []string{
			"*** Begin Patch\n*** Update File: /workspace/app.txt\n@@\n-old line\n+new line\n*** End Patch",
		},
		OutputNotes: "Returns a short per-file summary of applied add and update operations.",
		Interop: toolscommon.ToolInterop(
			toolName,
			toolscore.ToolBoundaryKindCustom,
			toolscore.ToolBoundaryKindFunction,
			toolName,
		),
		SafetyTags: []string{"mutating"},
		Mutating:   true,
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
	patch, err := parseApplyPatch(call.Input)
	if err != nil {
		toolCtx.SetParseError(step, err)
		return nil
	}
	var summaries []string
	for _, op := range patch.Ops {
		summary, err := applyPatchFileOp(toolCtx, op)
		if err != nil {
			toolCtx.SetPolicyError(step, err)
			return nil
		}
		summaries = append(summaries, summary)
	}
	var b strings.Builder
	b.WriteString("Patch applied successfully.\n")
	for idx, summary := range summaries {
		fmt.Fprintf(&b, "%d|%s\n", idx+1, summary)
	}
	toolCtx.SetOutput(step, b.String())
	return nil
}

func parseApplyPatch(input string) (parsedPatch, error) {
	lines := toolscommon.ScannerLines(strings.TrimSpace(input))
	if len(lines) == 0 {
		return parsedPatch{}, fmt.Errorf("patch must not be empty")
	}
	if lines[0] != "*** Begin Patch" {
		return parsedPatch{}, fmt.Errorf("patch must start with \"*** Begin Patch\"")
	}
	idx := 1
	var ops []patchFileOp
	for idx < len(lines) {
		line := lines[idx]
		if line == "*** End Patch" {
			if idx != len(lines)-1 {
				return parsedPatch{}, fmt.Errorf("unexpected content after \"*** End Patch\"")
			}
			return parsedPatch{Ops: ops}, nil
		}
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			op, next, err := parseAddFileOp(lines, idx)
			if err != nil {
				return parsedPatch{}, err
			}
			ops = append(ops, op)
			idx = next
		case strings.HasPrefix(line, "*** Update File: "):
			op, next, err := parseUpdateFileOp(lines, idx)
			if err != nil {
				return parsedPatch{}, err
			}
			ops = append(ops, op)
			idx = next
		default:
			return parsedPatch{}, fmt.Errorf("unexpected patch line %q", line)
		}
	}
	return parsedPatch{}, fmt.Errorf("patch is missing \"*** End Patch\"")
}

func parseAddFileOp(lines []string, start int) (patchFileOp, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[start], "*** Add File: "))
	if path == "" {
		return patchFileOp{}, start, fmt.Errorf("add file operation must include a path")
	}
	op := patchFileOp{Action: "add", Path: path}
	idx := start + 1
	for idx < len(lines) {
		line := lines[idx]
		if strings.HasPrefix(line, "*** ") {
			break
		}
		if !strings.HasPrefix(line, "+") {
			return patchFileOp{}, start, fmt.Errorf("add file %q line %q must start with +", path, line)
		}
		op.AddLines = append(op.AddLines, line[1:])
		idx++
	}
	if len(op.AddLines) == 0 {
		return patchFileOp{}, start, fmt.Errorf("add file %q must include at least one line", path)
	}
	return op, idx, nil
}

func parseUpdateFileOp(lines []string, start int) (patchFileOp, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[start], "*** Update File: "))
	if path == "" {
		return patchFileOp{}, start, fmt.Errorf("update file operation must include a path")
	}
	op := patchFileOp{Action: "update", Path: path}
	idx := start + 1
	for idx < len(lines) {
		line := lines[idx]
		if strings.HasPrefix(line, "*** ") {
			break
		}
		if !strings.HasPrefix(line, "@@") {
			return patchFileOp{}, start, fmt.Errorf("update file %q expected hunk header, got %q", path, line)
		}
		hunk := patchHunk{Header: strings.TrimSpace(strings.TrimPrefix(line, "@@"))}
		idx++
		for idx < len(lines) {
			line = lines[idx]
			if line == "*** End of File" {
				idx++
				break
			}
			if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "*** ") {
				break
			}
			if len(line) == 0 {
				return patchFileOp{}, start, fmt.Errorf("update file %q contains malformed empty change line", path)
			}
			kind := line[0]
			if kind != ' ' && kind != '+' && kind != '-' {
				return patchFileOp{}, start, fmt.Errorf("update file %q line %q must start with space, +, or -", path, line)
			}
			hunk.Lines = append(hunk.Lines, patchChangeLine{
				Kind: kind,
				Text: line[1:],
			})
			idx++
		}
		if len(hunk.Lines) == 0 {
			return patchFileOp{}, start, fmt.Errorf("update file %q hunk %q must include change lines", path, hunk.Header)
		}
		op.Hunks = append(op.Hunks, hunk)
	}
	return op, idx, nil
}

func applyPatchFileOp(toolCtx toolscore.ToolContext, op patchFileOp) (string, error) {
	resolved, err := toolCtx.ResolvePath(op.Path)
	if err != nil {
		return "", err
	}
	switch op.Action {
	case "add":
		return applyAddFileOp(toolCtx, resolved, op)
	case "update":
		return applyUpdateFileOp(toolCtx, resolved, op)
	default:
		return "", fmt.Errorf("unsupported patch operation %q", op.Action)
	}
}

func applyAddFileOp(toolCtx toolscore.ToolContext, resolved string, op patchFileOp) (string, error) {
	if _, err := toolCtx.FileSystem().Stat(resolved); err == nil {
		return "", fmt.Errorf("%q already exists", resolved)
	} else if !errors.Is(err, fs.ErrNotExist) && !os.IsNotExist(err) {
		return "", err
	}
	if err := toolCtx.FileSystem().MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return "", err
	}
	content := strings.Join(op.AddLines, "\n")
	if len(op.AddLines) > 0 {
		content += "\n"
	}
	if err := toolCtx.FileSystem().WriteFile(resolved, []byte(content), defaultApplyPatchPerm); err != nil {
		return "", err
	}
	return "add|" + filepath.ToSlash(resolved), nil
}

func applyUpdateFileOp(toolCtx toolscore.ToolContext, resolved string, op patchFileOp) (string, error) {
	info, err := toolCtx.FileSystem().Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory", resolved)
	}
	original, err := toolscommon.ReadTextFileLimited(toolCtx.FileSystem(), resolved, maxApplyPatchFileBytes)
	if err != nil {
		return "", err
	}
	if strings.ContainsRune(original, '\x00') {
		return "", fmt.Errorf("%q appears to be a binary file", resolved)
	}
	hadTrailingNewline := strings.HasSuffix(original, "\n")
	lines := toolscommon.SplitPreservingLineStructure(original)
	updated, err := applyPatchHunks(lines, op.Hunks)
	if err != nil {
		return "", fmt.Errorf("%s: %w", resolved, err)
	}
	content := strings.Join(updated, "\n")
	if hadTrailingNewline && len(updated) > 0 {
		content += "\n"
	}
	if err := toolCtx.FileSystem().WriteFile(resolved, []byte(content), patchFilePerm(info.Mode())); err != nil {
		return "", err
	}
	return "update|" + filepath.ToSlash(resolved), nil
}

func applyPatchHunks(lines []string, hunks []patchHunk) ([]string, error) {
	cursor := 0
	current := append([]string(nil), lines...)
	for _, hunk := range hunks {
		oldLines, newLines := patchHunkSlices(hunk)
		matchAt := findPatchMatch(current, oldLines, cursor)
		if matchAt < 0 {
			matchAt = findPatchMatch(current, oldLines, 0)
		}
		if matchAt < 0 {
			if strings.TrimSpace(hunk.Header) != "" {
				return nil, fmt.Errorf("could not locate hunk %q", hunk.Header)
			}
			return nil, fmt.Errorf("could not locate patch hunk")
		}
		current = spliceLines(current, matchAt, matchAt+len(oldLines), newLines)
		cursor = matchAt + len(newLines)
	}
	return current, nil
}

func patchHunkSlices(hunk patchHunk) ([]string, []string) {
	oldLines := make([]string, 0, len(hunk.Lines))
	newLines := make([]string, 0, len(hunk.Lines))
	for _, line := range hunk.Lines {
		if line.Kind != '+' {
			oldLines = append(oldLines, line.Text)
		}
		if line.Kind != '-' {
			newLines = append(newLines, line.Text)
		}
	}
	return oldLines, newLines
}

func findPatchMatch(lines, target []string, start int) int {
	if len(target) == 0 {
		if start < 0 {
			return 0
		}
		if start > len(lines) {
			return len(lines)
		}
		return start
	}
	if start < 0 {
		start = 0
	}
	for idx := start; idx+len(target) <= len(lines); idx++ {
		if patchLineSliceEqual(lines[idx:idx+len(target)], target) {
			return idx
		}
	}
	return -1
}

func patchLineSliceEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}

func spliceLines(lines []string, start, end int, replacement []string) []string {
	result := make([]string, 0, len(lines)-toolscommon.MaxInt(0, end-start)+len(replacement))
	result = append(result, lines[:start]...)
	result = append(result, replacement...)
	result = append(result, lines[end:]...)
	return result
}

func patchFilePerm(mode fs.FileMode) fs.FileMode {
	perm := mode.Perm()
	if perm == 0 {
		return defaultApplyPatchPerm
	}
	return perm
}
