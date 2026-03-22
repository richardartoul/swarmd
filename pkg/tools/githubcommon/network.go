package githubcommon

import (
	"slices"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

var requiredHosts = []interp.HostMatcher{
	{Glob: "api.github.com"},
}

var ciRequiredHosts = []interp.HostMatcher{
	{Glob: "api.github.com"},
	{Glob: "github.com"},
	{Glob: "*.githubusercontent.com"},
	{Glob: "*.actions.githubusercontent.com"},
	{Glob: "*.blob.core.windows.net"},
}

func RequiredHosts() []interp.HostMatcher {
	return slices.Clone(requiredHosts)
}

func CIRequiredHosts() []interp.HostMatcher {
	return slices.Clone(ciRequiredHosts)
}
