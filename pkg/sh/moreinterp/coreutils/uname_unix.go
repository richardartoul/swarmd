//go:build unix

package coreutils

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func hostUnameInfo() unameInfo {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err == nil {
		info := unameInfo{
			sysname:  unix.ByteSliceToString(uts.Sysname[:]),
			nodename: unix.ByteSliceToString(uts.Nodename[:]),
			release:  unix.ByteSliceToString(uts.Release[:]),
			version:  unix.ByteSliceToString(uts.Version[:]),
			machine:  unix.ByteSliceToString(uts.Machine[:]),
		}
		return fillMissingUnameInfo(info)
	}
	return fillMissingUnameInfo(unameInfo{})
}

func fillMissingUnameInfo(info unameInfo) unameInfo {
	if info.sysname == "" {
		info.sysname = fallbackSysname()
	}
	if info.nodename == "" {
		if hostname, err := os.Hostname(); err == nil && hostname != "" {
			info.nodename = hostname
		} else {
			info.nodename = "localhost"
		}
	}
	if info.release == "" {
		info.release = info.sysname
	}
	if info.version == "" {
		info.version = runtime.Version()
	}
	if info.machine == "" {
		info.machine = fallbackMachine()
	}
	return info
}
