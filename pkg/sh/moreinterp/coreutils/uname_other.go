//go:build !unix

package coreutils

import (
	"os"
	"runtime"
)

func hostUnameInfo() unameInfo {
	info := unameInfo{
		sysname: fallbackSysname(),
		release: fallbackSysname(),
		version: runtime.Version(),
		machine: fallbackMachine(),
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		info.nodename = hostname
	} else {
		info.nodename = "localhost"
	}
	return info
}
