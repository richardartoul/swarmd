// See LICENSE for licensing information

package agent

import (
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/richardartoul/swarmd/pkg/sh/sandbox"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

type switchWriter struct {
	mu     sync.RWMutex
	target io.Writer
}

func newSwitchWriter() *switchWriter {
	return &switchWriter{target: io.Discard}
}

func (w *switchWriter) Set(target io.Writer) {
	if target == nil {
		target = io.Discard
	}
	w.mu.Lock()
	w.target = target
	w.mu.Unlock()
}

func (w *switchWriter) Write(p []byte) (int, error) {
	w.mu.RLock()
	target := w.target
	w.mu.RUnlock()
	return target.Write(p)
}

type captureWriter struct {
	mu             sync.Mutex
	previewLimit   int
	spillThreshold int
	spill          *captureSpillConfig
	mirror         io.Writer
	buf            []byte
	truncated      bool
	totalBytes     int64
	spillFile      io.ReadWriteCloser
}

type captureSpillConfig struct {
	FileSystem  sandbox.FileSystem
	Path        string
	MimeType    string
	Description string
}

type captureSnapshot struct {
	Preview    string
	Truncated  bool
	TotalBytes int64
	File       *toolscore.FileReference
}

func newCaptureWriter(limit int, spillThreshold int, spill *captureSpillConfig, mirror io.Writer) *captureWriter {
	return &captureWriter{
		previewLimit:   limit,
		spillThreshold: spillThreshold,
		spill:          spill,
		mirror:         mirror,
	}
}

func (w *captureWriter) Write(p []byte) (int, error) {
	if w.mirror != nil {
		_, _ = w.mirror.Write(p)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if len(p) == 0 {
		return 0, nil
	}
	if err := w.writeSpillLocked(p); err != nil {
		return 0, err
	}
	w.totalBytes += int64(len(p))
	if w.previewLimit <= 0 {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	if len(w.buf) >= w.previewLimit {
		w.truncated = true
		return len(p), nil
	}
	remaining := w.previewLimit - len(w.buf)
	if len(p) > remaining {
		w.buf = append(w.buf, p[:remaining]...)
		w.truncated = true
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *captureWriter) Snapshot() (captureSnapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	snapshot := captureSnapshot{
		Preview:    string(w.buf),
		Truncated:  w.truncated,
		TotalBytes: w.totalBytes,
	}
	ref, err := w.finishSpillLocked()
	if err != nil {
		return captureSnapshot{}, err
	}
	snapshot.File = ref
	return snapshot, nil
}

func (w *captureWriter) writeSpillLocked(p []byte) error {
	if w.spill == nil || w.spill.FileSystem == nil || w.spill.Path == "" {
		return nil
	}
	if w.spillFile == nil {
		if err := w.spill.FileSystem.MkdirAll(filepath.Dir(w.spill.Path), 0o755); err != nil {
			return err
		}
		file, err := w.spill.FileSystem.OpenFile(w.spill.Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		w.spillFile = file
	}
	_, err := w.spillFile.Write(p)
	return err
}

func (w *captureWriter) finishSpillLocked() (*toolscore.FileReference, error) {
	if w.spillFile != nil {
		if err := w.spillFile.Close(); err != nil {
			return nil, err
		}
		w.spillFile = nil
	}
	if w.spill == nil || w.spill.FileSystem == nil || w.spill.Path == "" {
		return nil, nil
	}
	if w.totalBytes == 0 || (w.spillThreshold > 0 && w.totalBytes <= int64(w.spillThreshold)) {
		_ = w.spill.FileSystem.RemoveAll(w.spill.Path)
		return nil, nil
	}
	return &toolscore.FileReference{
		Path:        w.spill.Path,
		MimeType:    w.spill.MimeType,
		Description: w.spill.Description,
		SizeBytes:   w.totalBytes,
	}, nil
}
