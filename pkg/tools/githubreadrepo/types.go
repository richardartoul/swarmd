package githubreadrepo

import "github.com/richardartoul/swarmd/pkg/tools/githubcommon"

const (
	ToolName = "github_read_repo"

	ActionGetRepo           = "get_repo"
	ActionSearchCode        = "search_code"
	ActionListTree          = "list_tree"
	ActionGetFileContents   = "get_file_contents"
	ActionListBranches      = "list_branches"
	ActionGetBranch         = "get_branch"
	ActionGetRulesForBranch = "get_rules_for_branch"
	ActionListRulesets      = "list_rulesets"
	ActionGetRuleset        = "get_ruleset"
)

type input struct {
	Action          string   `json:"action"`
	Owner           string   `json:"owner"`
	Repo            string   `json:"repo"`
	Query           string   `json:"query,omitempty"`
	Ref             string   `json:"ref,omitempty"`
	Path            string   `json:"path,omitempty"`
	Branch          string   `json:"branch,omitempty"`
	Page            int      `json:"page,omitempty"`
	PerPage         int      `json:"per_page,omitempty"`
	Recursive       *bool    `json:"recursive,omitempty"`
	Protected       *bool    `json:"protected,omitempty"`
	IncludesParents *bool    `json:"includes_parents,omitempty"`
	Targets         []string `json:"targets,omitempty"`
	RulesetID       int64    `json:"ruleset_id,omitempty"`
}

type Input = input

type repoRequest struct {
	Action          string
	Repo            githubcommon.RepoCoordinates
	Query           string
	Ref             string
	Path            string
	Branch          string
	Pagination      githubcommon.Pagination
	Recursive       bool
	Protected       *bool
	IncludesParents *bool
	Targets         []string
	RulesetID       int64
}
