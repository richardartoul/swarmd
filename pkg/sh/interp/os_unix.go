// Copyright (c) 2017, Andrey Nering <andrey.nering@gmail.com>
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"context"
	"fmt"
	"os/user"
	"strconv"
	"syscall"

	"github.com/richardartoul/swarmd/pkg/sh/syntax"
	"golang.org/x/sys/unix"
)

func mkfifo(path string, mode uint32) error {
	return unix.Mkfifo(path, mode)
}

// access is similar to checking the permission bits from [io/fs.FileInfo],
// but it also takes into account the current user's role.
func (r *Runner) access(ctx context.Context, path string, mode uint32) error {
	// TODO(v4): "access" may need to become part of a handler, like "open" or "stat".
	if fsys, ok := r.fileSystem.(AccessFileSystem); ok {
		return fsys.Access(path, mode)
	}
	hostPath, err := fsHostPath(r.fileSystem, path)
	if err == nil {
		return unix.Access(hostPath, mode)
	}
	info, err := r.lstat(ctx, path)
	if err != nil {
		return err
	}
	m := info.Mode()
	switch mode {
	case AccessRead:
		if m&0o400 == 0 {
			return fmt.Errorf("file is not readable")
		}
	case AccessWrite:
		if m&0o200 == 0 {
			return fmt.Errorf("file is not writable")
		}
	case AccessExecute:
		if m&0o100 == 0 {
			return fmt.Errorf("file is not executable")
		}
	}
	return nil
}

// unTestOwnOrGrp implements the -O and -G unary tests. If the file does not
// exist, or the current user cannot be retrieved, returns false.
func (r *Runner) unTestOwnOrGrp(ctx context.Context, op syntax.UnTestOperator, x string) bool {
	info, err := r.stat(ctx, x)
	if err != nil {
		return false
	}
	u, err := user.Current()
	if err != nil {
		return false
	}
	fileUID, fileGID, ok := fsOwnerIDs(r.fileSystem, info)
	if !ok {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return false
		}
		fileUID = stat.Uid
		fileGID = stat.Gid
	}
	if op == syntax.TsUsrOwn {
		uid, _ := strconv.Atoi(u.Uid)
		return uint32(uid) == fileUID
	}
	gid, _ := strconv.Atoi(u.Gid)
	return uint32(gid) == fileGID
}

type waitStatus = syscall.WaitStatus
