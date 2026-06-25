package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// resolveMemoryDir accepts either a .memory directory path or a parent directory
// that contains .memory/.
func resolveMemoryDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("memory dir path must not be empty")
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("memory dir %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("memory dir %q is not a directory", path)
	}
	if filepath.Base(path) == ".memory" {
		return path, nil
	}
	nested := filepath.Join(path, ".memory")
	if st, err := os.Stat(nested); err == nil && st.IsDir() {
		return nested, nil
	}
	return path, nil
}

func loadMemoryState(srcDir, destMemoryDir string) error {
	if err := os.MkdirAll(destMemoryDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		src := filepath.Join(srcDir, ent.Name())
		dst := filepath.Join(destMemoryDir, ent.Name())
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", ent.Name(), err)
		}
	}
	return nil
}

func writebackMemoryState(srcMemoryDir, destDir string) error {
	destDir, err := resolveMemoryDir(destDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(srcMemoryDir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		src := filepath.Join(srcMemoryDir, ent.Name())
		dst := filepath.Join(destDir, ent.Name())
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("writeback %s: %w", ent.Name(), err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
