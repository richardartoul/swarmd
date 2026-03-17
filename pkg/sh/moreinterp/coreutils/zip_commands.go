package coreutils

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/richardartoul/swarmd/pkg/sh/internal"
	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

func runZip(env *commandEnv, args []string) error {
	recursive := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "r", Names: []string{"-r"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		if opt.Canonical == "r" {
			recursive = true
		}
	}
	if len(operands) < 2 {
		return fmt.Errorf("usage: zip [-r] archive.zip file...")
	}

	archivePath, err := env.resolvePathArg(operands[0])
	if err != nil {
		return err
	}
	inputs, err := env.resolvePaths(operands[1:])
	if err != nil {
		return err
	}

	archiveFile, err := env.hc.FileSystem.OpenFile(archivePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(archiveFile)

	for _, input := range inputs {
		if err := addZipInput(env, writer, input, archivePath, recursive); err != nil {
			_ = writer.Close()
			_ = archiveFile.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		_ = archiveFile.Close()
		return err
	}
	return archiveFile.Close()
}

func addZipInput(env *commandEnv, writer *zip.Writer, input resolvedPath, archivePath string, recursive bool) error {
	info, err := env.hc.FileSystem.Lstat(input.path)
	if err != nil {
		return err
	}
	if filepath.Clean(input.path) == filepath.Clean(archivePath) {
		return fmt.Errorf("zip: refusing to add archive to itself")
	}
	if info.IsDir() && !recursive {
		return fmt.Errorf("zip: %s is a directory (use -r to recurse)", input.raw)
	}
	if !info.IsDir() {
		return addZipEntry(env, writer, input.raw, input.path, info)
	}
	return walkResolved(env.hc.FileSystem, input.path, func(currentPath string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filepath.Clean(currentPath) == filepath.Clean(archivePath) {
			return nil
		}
		rel, err := filepath.Rel(input.path, currentPath)
		if err != nil {
			return err
		}
		return addZipEntry(env, writer, joinDisplayPath(input.raw, rel), currentPath, info)
	})
}

func addZipEntry(env *commandEnv, writer *zip.Writer, archiveName, sourcePath string, info fs.FileInfo) error {
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(archiveName)
	switch {
	case info.IsDir():
		if !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}
		header.Method = zip.Store
	case info.Mode()&os.ModeSymlink != 0:
		header.Method = zip.Store
	case info.Mode().IsRegular():
		header.Method = zip.Deflate
	default:
		return fmt.Errorf("zip: unsupported file type for %s", archiveName)
	}

	entryWriter, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		return nil
	case info.Mode()&os.ModeSymlink != 0:
		readlinkFS, err := env.readlinkFS()
		if err != nil {
			return err
		}
		target, err := readlinkFS.Readlink(sourcePath)
		if err != nil {
			return err
		}
		_, err = io.WriteString(entryWriter, target)
		return err
	default:
		file, err := env.hc.FileSystem.Open(sourcePath)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(entryWriter, file)
		return err
	}
}

func runUnzip(env *commandEnv, args []string) error {
	destRaw := "."
	listOnly := false
	parsedOpts, operands, err := parseUtilityOptions(args, []internal.OptionSpec{
		{Canonical: "d", Names: []string{"-d"}, ValueMode: internal.RequiredOptionValue},
		{Canonical: "l", Names: []string{"-l"}},
	})
	if err != nil {
		return err
	}
	for _, opt := range parsedOpts {
		switch opt.Canonical {
		case "d":
			destRaw = opt.Value
		case "l":
			listOnly = true
		}
	}
	if len(operands) != 1 {
		return fmt.Errorf("usage: unzip [-d directory] archive.zip")
	}

	archivePath, err := env.resolvePathArg(operands[0])
	if err != nil {
		return err
	}
	archiveFile, err := env.hc.FileSystem.Open(archivePath)
	if err != nil {
		return err
	}
	defer archiveFile.Close()
	data, err := io.ReadAll(archiveFile)
	if err != nil {
		return err
	}

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	if listOnly {
		for _, file := range reader.File {
			if _, err := fmt.Fprintln(env.stdout(), file.Name); err != nil {
				return err
			}
		}
		return nil
	}

	destPath, err := env.resolvePathArg(destRaw)
	if err != nil {
		return err
	}
	mutableFS, err := env.mutableFS()
	if err != nil {
		return err
	}
	if err := mutableFS.MkdirAll(destPath, os.ModePerm); err != nil {
		return err
	}
	chmodFS, _ := env.chmodFS()
	for _, file := range reader.File {
		if err := extractZipFile(env, file, destPath, mutableFS, chmodFS); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(env *commandEnv, file *zip.File, destPath string, mutableFS interp.MutableFileSystem, chmodFS interp.ChmodFileSystem) error {
	targetPath, err := safeJoin(destPath, file.Name)
	if err != nil {
		fmt.Fprintf(env.stderr(), "Warning: Skipping file %q due to: %v\n", file.Name, err)
		return nil
	}
	mode := file.Mode()
	if file.FileInfo().IsDir() || strings.HasSuffix(file.Name, "/") {
		if err := mutableFS.MkdirAll(targetPath, mode.Perm()); err != nil {
			return err
		}
		if chmodFS != nil {
			if err := chmodFS.Chmod(targetPath, mode); err != nil {
				return err
			}
		}
		if !file.Modified.IsZero() {
			if err := mutableFS.Chtimes(targetPath, file.Modified, file.Modified); err != nil {
				return err
			}
		}
		return nil
	}
	if err := mutableFS.MkdirAll(filepath.Dir(targetPath), os.ModePerm); err != nil {
		return err
	}
	reader, err := file.Open()
	if err != nil {
		return err
	}
	defer reader.Close()

	if mode&os.ModeSymlink != 0 {
		target, err := io.ReadAll(reader)
		if err != nil {
			return err
		}
		return mutableFS.Symlink(string(target), targetPath)
	}
	if mode&os.ModeType != 0 && !mode.IsRegular() {
		return fmt.Errorf("unzip: unsupported file type for %s", file.Name)
	}

	outputFile, err := env.hc.FileSystem.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(outputFile, reader); err != nil {
		outputFile.Close()
		return err
	}
	if err := outputFile.Close(); err != nil {
		return err
	}
	if chmodFS != nil {
		if err := chmodFS.Chmod(targetPath, mode); err != nil {
			return err
		}
	}
	if !file.Modified.IsZero() {
		if err := mutableFS.Chtimes(targetPath, file.Modified, file.Modified); err != nil {
			return err
		}
	}
	return nil
}
