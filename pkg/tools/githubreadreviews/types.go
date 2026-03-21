package githubreadreviews

import "github.com/richardartoul/swarmd/pkg/tools/githubcommon"

const (
	ToolName = "github_read_reviews"

	ActionSearchIssues                 = "search_issues"
	ActionListIssues                   = "list_issues"
	ActionGetIssue                     = "get_issue"
	ActionListIssueComments            = "list_issue_comments"
	ActionGetIssueTimeline             = "get_issue_timeline"
	ActionListPullRequests             = "list_pull_requests"
	ActionGetPullRequest               = "get_pull_request"
	ActionListPullRequestFiles         = "list_pull_request_files"
	ActionListPullRequestCommits       = "list_pull_request_commits"
	ActionListPullRequestReviews       = "list_pull_request_reviews"
	ActionListPullRequestReviewComments = "list_pull_request_review_comments"
	ActionListCommits                  = "list_commits"
	ActionGetCommit                    = "get_commit"
	ActionListCommitsForPath           = "list_commits_for_path"
	ActionCompareRefs                  = "compare_refs"
	ActionListPullRequestsForCommit    = "list_pull_requests_for_commit"
)

type input struct {
	Action      string   `json:"action"`
	Owner       string   `json:"owner"`
	Repo        string   `json:"repo"`
	Query       string   `json:"query,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	State       string   `json:"state,omitempty"`
	Base        string   `json:"base,omitempty"`
	Head        string   `json:"head,omitempty"`
	Sha         string   `json:"sha,omitempty"`
	Ref         string   `json:"ref,omitempty"`
	Path        string   `json:"path,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Page        int      `json:"page,omitempty"`
	PerPage     int      `json:"per_page,omitempty"`
	IssueNumber int64    `json:"issue_number,omitempty"`
	PullNumber  int64    `json:"pull_number,omitempty"`
	CommitSHA   string   `json:"commit_sha,omitempty"`
}

type Input = input

type reviewRequest struct {
	Action      string
	Repo        githubcommon.RepoCoordinates
	Query       string
	Kind        string
	State       string
	Base        string
	Head        string
	Sha         string
	Ref         string
	Path        string
	Labels      []string
	Pagination  githubcommon.Pagination
	IssueNumber int64
	PullNumber  int64
	CommitSHA   string
}
