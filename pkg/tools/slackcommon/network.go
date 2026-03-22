package slackcommon

import (
	"slices"

	"github.com/richardartoul/swarmd/pkg/sh/interp"
)

var requiredHosts = []interp.HostMatcher{
	{Glob: "slack.com"},
}

func RequiredHosts() []interp.HostMatcher {
	return slices.Clone(requiredHosts)
}
