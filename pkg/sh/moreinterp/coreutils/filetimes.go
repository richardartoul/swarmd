package coreutils

import (
	"io/fs"
	"time"
)

// fileTimes best-effort preserves existing timestamps when a utility only
// intends to update one of them. Generic fs.FileInfo does not expose atime, so
// we fall back to mtime for both values when no richer stat data is available.
func fileTimes(info fs.FileInfo) (time.Time, time.Time) {
	mtime := info.ModTime()
	return mtime, mtime
}
