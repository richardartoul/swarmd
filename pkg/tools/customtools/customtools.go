package customtools

import (
	_ "github.com/richardartoul/swarmd/pkg/tools/datadogread"
	_ "github.com/richardartoul/swarmd/pkg/tools/githubreadci"
	_ "github.com/richardartoul/swarmd/pkg/tools/githubreadrepo"
	_ "github.com/richardartoul/swarmd/pkg/tools/githubreadreviews"
	_ "github.com/richardartoul/swarmd/pkg/tools/serverlog"
	_ "github.com/richardartoul/swarmd/pkg/tools/slackdm"
	_ "github.com/richardartoul/swarmd/pkg/tools/slackhistory"
	_ "github.com/richardartoul/swarmd/pkg/tools/slackpost"
	_ "github.com/richardartoul/swarmd/pkg/tools/slackreplies"
)
