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

hunk: add_hunk | update_hunk | delete_hunk
add_hunk: "*** Add File: " filename LF add_line+
update_hunk: "*** Update File: " filename LF change?
delete_hunk: "*** Delete File: " filename LF

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

type patchAction string

const (
	patchActionAdd    patchAction = "add"
	patchActionUpdate patchAction = "update"
	patchActionDelete patchAction = "delete"
)

type patchFileOp struct {
	Action   patchAction
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
		Description: "Apply a structured patch that adds, updates, or deletes local files.",
		Kind:        toolscore.ToolKindCustom,
		Strict:      true,
		CustomFormat: &toolscore.ToolFormat{
			Type:       "grammar",
			Syntax:     "lark",
			Definition: grammar,
		},
		Examples: []string{
			"*** Begin Patch\n*** Update File: /workspace/app.txt\n@@\n-old line\n+new line\n*** End Patch",
			"*** Begin Patch\n*** Delete File: /workspace/obsolete.txt\n*** End Patch",
		},
		OutputNotes: "Returns a short per-file summary of applied add, update, and delete operations.",
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
	lines := patchLines(input)
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
		case strings.HasPrefix(line, "*** Delete File: "):
			op, err := parseDeleteFileOp(line)
			if err != nil {
				return parsedPatch{}, err
			}
			ops = append(ops, op)
			idx++
		default:
			return parsedPatch{}, fmt.Errorf("unexpected patch line %q", line)
		}
	}
	return parsedPatch{}, fmt.Errorf("patch is missing \"*** End Patch\"")
}

// patchLines splits the raw patch into lines, trimming only the outer
// envelope: interior lines keep their exact content because leading and
// trailing whitespace inside change lines is significant.
func patchLines(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	lines := strings.Split(input, "\n")
	for idx, line := range lines {
		lines[idx] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

func parseDeleteFileOp(line string) (patchFileOp, error) {
	path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
	if path == "" {
		return patchFileOp{}, fmt.Errorf("delete file operation must include a path")
	}
	return patchFileOp{Action: patchActionDelete, Path: path}, nil
}

func parseAddFileOp(lines []string, start int) (patchFileOp, int, error) {
	path := strings.TrimSpace(strings.TrimPrefix(lines[start], "*** Add File: "))
	if path == "" {
		return patchFileOp{}, start, fmt.Errorf("add file operation must include a path")
	}
	op := patchFileOp{Action: patchActionAdd, Path: path}
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
	op := patchFileOp{Action: patchActionUpdate, Path: path}
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
	case patchActionAdd:
		return applyAddFileOp(toolCtx, resolved, op)
	case patchActionUpdate:
		return applyUpdateFileOp(toolCtx, resolved, op)
	case patchActionDelete:
		return applyDeleteFileOp(toolCtx, resolved)
	default:
		return "", fmt.Errorf("unsupported patch operation %q", op.Action)
	}
}

func applyDeleteFileOp(toolCtx toolscore.ToolContext, resolved string) (string, error) {
	info, err := toolCtx.FileSystem().Stat(resolved)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory; apply_patch only deletes files", resolved)
	}
	if err := toolCtx.FileSystem().RemoveAll(resolved); err != nil {
		return "", err
	}
	return "delete|" + filepath.ToSlash(resolved), nil
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
	content := strings.Join(op.AddLines, "\n") + "\n"
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
	result := make([]string, 0, len(lines)-max(0, end-start)+len(replacement))
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
