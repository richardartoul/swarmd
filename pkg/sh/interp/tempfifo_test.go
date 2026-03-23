// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type trackingOpenFS struct {
	OSFileSystem
	opens int
}

func (f *trackingOpenFS) OpenFile(name string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	f.opens++
	return nopReadWriteCloser{Reader: strings.NewReader("adapter")}, nil
}

type hostlessTrackingOpenFS struct {
	trackingOpenFS
}

func (hostlessTrackingOpenFS) HostPath(string) (string, error) {
	return "", ErrNoHostPath
}

type nopReadWriteCloser struct {
	io.Reader
}

func (nopReadWriteCloser) Write([]byte) (int, error) { return 0, nil }
func (nopReadWriteCloser) Close() error              { return nil }

func TestRunnerOpenUsesOSForRememberedTempFIFO(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "sh-interp-remembered")
	if err := os.WriteFile(path, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}

	fsys := &trackingOpenFS{}
	r := &Runner{
		usedNew:       true,
		didReset:      true,
		Dir:           tempDir,
		fileSystem:    fsys,
		tempFIFOPaths: newRunnerTempFIFOSet(),
	}
	r.rememberTempFIFO(path)

	f, err := r.open(context.Background(), path, os.O_RDONLY, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "host" {
		t.Fatalf("got %q, want %q", data, "host")
	}
	if fsys.opens != 0 {
		t.Fatalf("configured filesystem OpenFile calls = %d, want 0", fsys.opens)
	}
}

func TestRunnerOpenUsesConfiguredFSAfterForgettingTempFIFO(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "sh-interp-forgotten")

	fsys := &trackingOpenFS{}
	r := &Runner{
		usedNew:       true,
		didReset:      true,
		Dir:           tempDir,
		fileSystem:    fsys,
		tempFIFOPaths: newRunnerTempFIFOSet(),
	}
	r.rememberTempFIFO(path)
	r.forgetTempFIFO(path)

	f, err := r.open(context.Background(), path, os.O_RDONLY, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "adapter" {
		t.Fatalf("got %q, want %q", data, "adapter")
	}
	if fsys.opens != 1 {
		t.Fatalf("configured filesystem OpenFile calls = %d, want 1", fsys.opens)
	}
}

func TestRunnerOpenUsesConfiguredFSForRememberedTempFIFOWithoutHostPath(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "sh-interp-no-host-path")

	fsys := &hostlessTrackingOpenFS{}
	r := &Runner{
		usedNew:       true,
		didReset:      true,
		Dir:           tempDir,
		fileSystem:    fsys,
		tempFIFOPaths: newRunnerTempFIFOSet(),
	}
	r.rememberTempFIFO(path)

	f, err := r.open(context.Background(), path, os.O_RDONLY, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "adapter" {
		t.Fatalf("got %q, want %q", data, "adapter")
	}
	if fsys.opens != 1 {
		t.Fatalf("configured filesystem OpenFile calls = %d, want 1", fsys.opens)
	}
}

func TestRunnerOpenTempFIFOUsesOSWhenHostPathAvailable(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "sh-interp-helper-host-path")
	if err := os.WriteFile(path, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}

	fsys := &trackingOpenFS{}
	r := &Runner{
		usedNew:       true,
		didReset:      true,
		Dir:           tempDir,
		fileSystem:    fsys,
		tempFIFOPaths: newRunnerTempFIFOSet(),
	}
	r.rememberTempFIFO(path)

	f, err := r.openTempFIFO(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "host" {
		t.Fatalf("got %q, want %q", data, "host")
	}
	if fsys.opens != 0 {
		t.Fatalf("configured filesystem OpenFile calls = %d, want 0", fsys.opens)
	}
}
