// See LICENSE for licensing information

package agent

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const (
	serverRunIDMetadataKey = "server_run_id"
	outputSpillDirName     = "tool-outputs"
)

func outputSpillBaseDir(fileSystem sandbox.FileSystem, sandboxRoot string) string {
	tempRoot := filepath.Join(sandboxRoot, ".tmp")
	if tempDirProvider, ok := any(fileSystem).(interface{ TempDir() string }); ok {
		if tempDir := strings.TrimSpace(tempDirProvider.TempDir()); tempDir != "" {
			tempRoot = tempDir
		}
	}
	return filepath.Join(tempRoot, outputSpillDirName)
}

// SweepStaleOutputSpillDirs removes any leftover runtime spill directories for a sandbox root.
func SweepStaleOutputSpillDirs(fileSystem sandbox.FileSystem) error {
	if fileSystem == nil {
		return fmt.Errorf("spill cleanup requires a filesystem")
	}
	sandboxRoot, err := fileSystem.Getwd()
	if err != nil {
		return fmt.Errorf("resolve sandbox root for spill cleanup: %w", err)
	}
	return fileSystem.RemoveAll(outputSpillBaseDir(fileSystem, sandboxRoot))
}

func (a *Agent) beginRunSpillDir(trigger Trigger) (func(), error) {
	runDir := filepath.Join(a.spillBaseDir, deriveRunOutputID(trigger))
	if err := a.fileSystem.RemoveAll(runDir); err != nil {
		return nil, fmt.Errorf("clear run spill directory %q: %w", runDir, err)
	}
	if err := a.fileSystem.MkdirAll(runDir, 0o755); err != nil {
		return nil, fmt.Errorf("create run spill directory %q: %w", runDir, err)
	}
	previous := a.currentRunSpillDir
	a.currentRunSpillDir = runDir
	return func() {
		a.currentRunSpillDir = previous
		_ = a.fileSystem.RemoveAll(runDir)
	}, nil
}

func (a *Agent) stepSpillFile(stepIndex int, fileName, mimeType, description string) (string, toolscore.FileReference, error) {
	if strings.TrimSpace(a.currentRunSpillDir) == "" {
		return "", toolscore.FileReference{}, fmt.Errorf("run spill directory is not initialized")
	}
	stepDir := filepath.Join(a.currentRunSpillDir, fmt.Sprintf("step_%d", stepIndex))
	if err := a.fileSystem.MkdirAll(stepDir, 0o755); err != nil {
		return "", toolscore.FileReference{}, fmt.Errorf("create spill step directory %q: %w", stepDir, err)
	}
	path := filepath.Join(stepDir, fileName)
	return path, toolscore.FileReference{
		Path:        path,
		MimeType:    strings.TrimSpace(mimeType),
		Description: strings.TrimSpace(description),
	}, nil
}

func (a *Agent) writeStepSpillFile(stepIndex int, fileName, mimeType, description string, data []byte) (*toolscore.FileReference, error) {
	path, ref, err := a.stepSpillFile(stepIndex, fileName, mimeType, description)
	if err != nil {
		return nil, err
	}
	if err := a.fileSystem.WriteFile(path, data, 0o644); err != nil {
		return nil, fmt.Errorf("write spill file %q: %w", path, err)
	}
	ref.SizeBytes = int64(len(data))
	return &ref, nil
}

func (a *Agent) outputFileThreshold() int {
	if a.outputFileThresholdBytes > 0 {
		return a.outputFileThresholdBytes
	}
	return a.maxOutputBytes
}

func deriveRunOutputID(trigger Trigger) string {
	if trigger.Metadata != nil {
		if raw, ok := trigger.Metadata[serverRunIDMetadataKey]; ok {
			if id := sanitizeSpillPathSegment(fmt.Sprint(raw)); id != "" {
				return id
			}
		}
	}
	if id := sanitizeSpillPathSegment(trigger.ID); id != "" {
		return fmt.Sprintf("%s-%d", id, time.Now().UTC().UnixNano())
	}
	return fmt.Sprintf("run-%d", time.Now().UTC().UnixNano())
}

func sanitizeSpillPathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-', r == '_', r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}
