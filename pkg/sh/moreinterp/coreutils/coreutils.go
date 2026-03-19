// Copyright (c) 2025, Andrey Nering <andrey@nering.com.br>
// See LICENSE for licensing information

// Package coreutils provides a middleware for the interpreter that handles
// in-process core utils like awk, cat, chmod, comm, cp, curl, cut, date, diff,
// env, find, grep, head, jq, ls, mkdir, mv, rm, sed, sort, tail, touch, tr,
// uname, uniq, wc, xargs, zip, and unzip.
//
// This is particularly useful to keep the max compability on Windows where
// these core utils are not available, unless when installed manually by the
// user.
package coreutils

import (
	"context"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

// CommandInfo describes an in-process command exposed by [ExecHandler].
type CommandInfo struct {
	Name            string
	RequiresNetwork bool
}

type commandSpec struct {
	info CommandInfo
	run  commandFunc
}

var commandSpecs = []commandSpec{
	{info: CommandInfo{Name: "awk"}, run: runAwk},
	{info: CommandInfo{Name: "base64"}, run: runBase64},
	{info: CommandInfo{Name: "cat"}, run: runCat},
	{info: CommandInfo{Name: "chmod"}, run: runChmod},
	{info: CommandInfo{Name: "comm"}, run: runComm},
	{info: CommandInfo{Name: "cp"}, run: runCp},
	{info: CommandInfo{Name: "curl", RequiresNetwork: true}, run: runCurl},
	{info: CommandInfo{Name: "cut"}, run: runCut},
	{info: CommandInfo{Name: "date"}, run: runDate},
	{info: CommandInfo{Name: "diff"}, run: runDiff},
	{info: CommandInfo{Name: "env"}, run: runEnv},
	{info: CommandInfo{Name: "find"}, run: runFind},
	{info: CommandInfo{Name: "grep"}, run: runGrep},
	{info: CommandInfo{Name: "gzcat"}, run: runGzcat},
	{info: CommandInfo{Name: "gunzip"}, run: runGunzip},
	{info: CommandInfo{Name: "gzip"}, run: runGzip},
	{info: CommandInfo{Name: "head"}, run: runHead},
	{info: CommandInfo{Name: "jq"}, run: runJq},
	{info: CommandInfo{Name: "ls"}, run: runLs},
	{info: CommandInfo{Name: "mkdir"}, run: runMkdir},
	{info: CommandInfo{Name: "mktemp"}, run: runMktemp},
	{info: CommandInfo{Name: "mv"}, run: runMv},
	{info: CommandInfo{Name: "rm"}, run: runRm},
	{info: CommandInfo{Name: "sed"}, run: runSed},
	{info: CommandInfo{Name: "shasum"}, run: runShasum},
	{info: CommandInfo{Name: "sort"}, run: runSort},
	{info: CommandInfo{Name: "tar"}, run: runTar},
	{info: CommandInfo{Name: "tail"}, run: runTail},
	{info: CommandInfo{Name: "touch"}, run: runTouch},
	{info: CommandInfo{Name: "tr"}, run: runTr},
	{info: CommandInfo{Name: "uname"}, run: runUname},
	{info: CommandInfo{Name: "unzip"}, run: runUnzip},
	{info: CommandInfo{Name: "uniq"}, run: runUniq},
	{info: CommandInfo{Name: "wc"}, run: runWc},
	{info: CommandInfo{Name: "xargs"}, run: runXargs},
	{info: CommandInfo{Name: "zip"}, run: runZip},
}

var commandNames = func() []string {
	names := make([]string, len(commandSpecs))
	for i, spec := range commandSpecs {
		names[i] = spec.info.Name
	}
	return names
}()

var commandSpecsByName = func() map[string]commandSpec {
	specs := make(map[string]commandSpec, len(commandSpecs))
	for _, spec := range commandSpecs {
		specs[spec.info.Name] = spec
	}
	return specs
}()

// Commands returns the in-process commands supported by [ExecHandler].
func Commands() []CommandInfo {
	infos := make([]CommandInfo, len(commandSpecs))
	for i, spec := range commandSpecs {
		infos[i] = spec.info
	}
	return infos
}

func lookupCommand(name string) (commandFunc, bool) {
	spec, ok := commandSpecsByName[name]
	if !ok {
		return nil, false
	}
	return spec.run, true
}

// ExecHandler returns an [interp.ExecHandlerFunc] middleware that handles core
// utils commands.
//
// Keep in mind that this middleware has priority over the core utils available
// by the system. You may want to use only on Windows to ensure that the system
// core utils are used on other platforms, like macOS and Linux.
func ExecHandler(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		program, programArgs := args[0], args[1:]
		cmd, ok := lookupCommand(program)
		if !ok {
			return next(ctx, args)
		}

		if err := cmd(newCommandEnv(ctx, next), programArgs); err != nil {
			return &Error{err: err}
		}
		return nil
	}
}
