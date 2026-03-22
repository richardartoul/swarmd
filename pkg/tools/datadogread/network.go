package datadogread

import (
	"slices"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

var requiredHosts = []interp.HostMatcher{
	{Glob: "*.datadoghq.com"},
	{Glob: "*.datadoghq.eu"},
	{Glob: "*.ddog-gov.com"},
}

func RequiredHosts() []interp.HostMatcher {
	return slices.Clone(requiredHosts)
}
