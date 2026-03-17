// See LICENSE for licensing information

package agent

import (
	"io"
	"sync"
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
	mu        sync.Mutex
	limit     int
	mirror    io.Writer
	buf       []byte
	truncated bool
}

func newCaptureWriter(limit int, mirror io.Writer) *captureWriter {
	return &captureWriter{
		limit:  limit,
		mirror: mirror,
	}
}

func (w *captureWriter) Write(p []byte) (int, error) {
	if w.mirror != nil {
		_, _ = w.mirror.Write(p)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.limit <= 0 {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	if len(w.buf) >= w.limit {
		w.truncated = true
		return len(p), nil
	}
	remaining := w.limit - len(w.buf)
	if len(p) > remaining {
		w.buf = append(w.buf, p[:remaining]...)
		w.truncated = true
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *captureWriter) Snapshot() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf), w.truncated
}
