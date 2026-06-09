package store

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func ensureReadOnlySQLiteExists(dsn string) error {
	path, ok, err := sqliteLocalPathAbs(dsn)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	info, err := os.Stat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("sqlite database %q is a directory", path)
		}
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sqlite database %q does not exist", path)
	}
	return fmt.Errorf("stat sqlite database %q: %w", path, err)
}

func sqliteLocalPath(dsn string) (string, bool) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" || dsn == ":memory:" {
		return "", false
	}
	if !strings.HasPrefix(dsn, "file:") {
		return dsn, true
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "file" {
		return "", false
	}
	if strings.EqualFold(parsed.Query().Get("mode"), "memory") {
		return "", false
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" {
		path = strings.TrimSpace(parsed.Opaque)
	}
	if path == "" || path == ":memory:" || strings.HasPrefix(path, ":memory:") {
		return "", false
	}
	return path, true
}

func sqliteLocalPathAbs(dsn string) (string, bool, error) {
	path, ok := sqliteLocalPath(dsn)
	if !ok {
		return "", false, nil
	}
	if filepath.IsAbs(path) {
		return path, true, nil
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve sqlite path %q: %w", path, err)
	}
	return absPath, true, nil
}

func normalizeSQLiteDSNWithMode(dsn string, readOnly bool) string {
	if dsn == ":memory:" {
		return dsn
	}
	if !strings.HasPrefix(dsn, "file:") {
		if abs, err := filepath.Abs(dsn); err == nil {
			dsn = abs
		}
		dsn = (&url.URL{Scheme: "file", Path: dsn}).String()
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	query := parsed.Query()
	if readOnly && query.Get("mode") == "" {
		query.Set("mode", "ro")
	}
	query.Add("_pragma", "foreign_keys(ON)")
	if !readOnly {
		query.Add("_pragma", "journal_mode(WAL)")
		query.Add("_pragma", "synchronous(NORMAL)")
	}
	query.Add("_pragma", "busy_timeout(5000)")
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
