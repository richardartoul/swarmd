package githubreadreviews

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/richardartoul/swarmd/pkg/server"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	"github.com/richardartoul/swarmd/pkg/tools/githubcommon"
)

var registerOnce sync.Once

type plugin struct {
	host server.ToolHost
}

type labelName struct {
	Name string `json:"name"`
}

type userLogin struct {
	Login string `json:"login"`
}

type teamSlug struct {
	Slug string `json:"slug"`
}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		server.RegisterTool(func(host server.ToolHost) toolscore.ToolPlugin {
			return plugin{host: host}
		}, server.ToolRegistrationOptions{
			RequiredEnv: []string{githubcommon.GitHubTokenEnvVar},
		})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        ToolName,
		Description: "Read GitHub issues, pull requests, reviews, comments, commits, comparisons, and issue timelines through a server-owned GitHub client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"action": toolscommon.StringEnumSchema(
					"GitHub review and history read action to execute.",
					ActionSearchIssues,
					ActionListIssues,
					ActionGetIssue,
					ActionListIssueComments,
					ActionGetIssueTimeline,
					ActionListPullRequests,
					ActionGetPullRequest,
					ActionListPullRequestFiles,
					ActionListPullRequestCommits,
					ActionListPullRequestReviews,
					ActionListPullRequestReviewComments,
					ActionListCommits,
					ActionGetCommit,
					ActionListCommitsForPath,
					ActionCompareRefs,
					ActionListPullRequestsForCommit,
				),
				"owner": toolscommon.StringSchema("GitHub repository owner or organization."),
				"repo":  toolscommon.StringSchema("GitHub repository name."),
				"query": toolscommon.StringSchema(`Required for "search_issues". The tool automatically scopes the search to the repository.`),
				"kind": toolscommon.StringEnumSchema(
					`Optional search kind for "search_issues". Use "issues", "pulls", or "all".`,
					"issues",
					"pulls",
					"all",
				),
				"state": toolscommon.StringSchema(`Issue or pull request state filter for list actions.`),
				"base":  toolscommon.StringSchema(`Base ref filter for "list_pull_requests" or base ref for "compare_refs".`),
				"head":  toolscommon.StringSchema(`Head ref for "compare_refs".`),
				"sha":   toolscommon.StringSchema(`Ref filter for "list_commits" or "list_commits_for_path".`),
				"ref":   toolscommon.StringSchema(`Commit ref or SHA for "get_commit".`),
				"path":  toolscommon.StringSchema(`Repository path for "list_commits_for_path".`),
				"labels": githubcommon.StringArraySchema(
					`Optional issue label filter for "list_issues".`,
				),
				"page":         toolscommon.IntegerSchema(`Optional page number for paginated actions.`),
				"per_page":     toolscommon.IntegerSchema(`Optional page size for paginated actions. Values above 100 are capped.`),
				"issue_number": toolscommon.IntegerSchema(`Issue number for issue reads.`),
				"pull_number":  toolscommon.IntegerSchema(`Pull request number for pull request reads.`),
				"commit_sha":   toolscommon.StringSchema(`Commit SHA for "list_pull_requests_for_commit".`),
			},
			"action",
			"owner",
			"repo",
		),
		RequiredArguments: []string{"action", "owner", "repo"},
		Examples: []string{
			`{"action":"search_issues","owner":"acme","repo":"monorepo","query":"label:flaky-test state:open","kind":"issues","page":1,"per_page":20}`,
			`{"action":"get_pull_request","owner":"acme","repo":"monorepo","pull_number":9021}`,
			`{"action":"compare_refs","owner":"acme","repo":"monorepo","base":"main","head":"fix/flaky-payment-retry"}`,
		},
		OutputNotes:     "Returns normalized JSON describing GitHub issues, pull requests, review state, and commit history.",
		SafetyTags:      []string{"network", "read_only"},
		RequiresNetwork: true,
		ReadOnly:        true,
	}
}

func (p plugin) NewHandler(config toolscore.ConfiguredTool) (toolscore.ToolHandler, error) {
	if err := toolscommon.ValidateNoToolConfig(ToolName, config.Config); err != nil {
		return nil, err
	}
	return toolscore.ToolHandlerFunc(p.handle), nil
}

func (p plugin) handle(ctx context.Context, toolCtx toolscore.ToolContext, step *toolscore.Step, call *toolscore.ToolAction) error {
	input, err := toolscore.DecodeToolInput[input](call.Input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	if !p.host.NetworkEnabled(toolCtx) {
		toolCtx.SetPolicyError(step, fmt.Errorf("%s requires network.reachable_hosts to be configured", ToolName))
		return nil
	}
	req, err := normalizeReadRequest(input)
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	runtime, err := p.host.Runtime(toolCtx)
	if err != nil {
		return err
	}
	client, err := githubcommon.NewClientFromLookup(runtime.LookupEnv, p.host.HTTPClient(toolCtx, toolscore.ToolHTTPClientOptions{
		ConnectTimeout:  10 * time.Second,
		FollowRedirects: true,
	}))
	if err != nil {
		toolCtx.SetPolicyError(step, err)
		return nil
	}
	response, err := executeRead(ctx, client, req)
	if err != nil {
		var apiErr *githubcommon.APIError
		if errors.As(err, &apiErr) {
			response = githubcommon.FailureResponse(ToolName, req.Action, apiErr, nil, nil, nil)
		} else {
			toolCtx.SetPolicyError(step, err)
			return nil
		}
	}
	output, err := toolscommon.MarshalToolOutput(response)
	if err != nil {
		return err
	}
	toolCtx.SetOutput(step, output)
	return nil
}

func normalizeReadRequest(input input) (reviewRequest, error) {
	coords, err := githubcommon.NormalizeRepoCoordinates(input.Owner, input.Repo)
	if err != nil {
		return reviewRequest{}, err
	}
	action := strings.TrimSpace(input.Action)
	if action == "" {
		return reviewRequest{}, fmt.Errorf("action must not be empty")
	}
	req := reviewRequest{
		Action:      action,
		Repo:        coords,
		Query:       strings.TrimSpace(input.Query),
		Kind:        strings.ToLower(strings.TrimSpace(input.Kind)),
		State:       strings.TrimSpace(input.State),
		Base:        strings.TrimSpace(input.Base),
		Head:        strings.TrimSpace(input.Head),
		Sha:         strings.TrimSpace(input.Sha),
		Ref:         strings.TrimSpace(input.Ref),
		Labels:      githubcommon.NormalizeStringSlice(input.Labels),
		IssueNumber: input.IssueNumber,
		PullNumber:  input.PullNumber,
		CommitSHA:   strings.TrimSpace(input.CommitSHA),
	}
	if strings.TrimSpace(input.Path) != "" {
		req.Path, err = githubcommon.NormalizeRepoPath(input.Path)
		if err != nil {
			return reviewRequest{}, err
		}
	}
	present := map[string]bool{
		"query":        req.Query != "",
		"kind":         req.Kind != "",
		"state":        req.State != "",
		"base":         req.Base != "",
		"head":         req.Head != "",
		"sha":          req.Sha != "",
		"ref":          req.Ref != "",
		"path":         req.Path != "",
		"labels":       len(req.Labels) > 0,
		"page":         input.Page != 0,
		"per_page":     input.PerPage != 0,
		"issue_number": req.IssueNumber != 0,
		"pull_number":  req.PullNumber != 0,
		"commit_sha":   req.CommitSHA != "",
	}
	switch action {
	case ActionSearchIssues:
		if err := githubcommon.RejectUnexpectedFields(action, present, "query", "kind", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		if req.Query == "" {
			return reviewRequest{}, fmt.Errorf("query must not be empty")
		}
		if req.Kind == "" {
			req.Kind = "issues"
		}
		if req.Kind != "issues" && req.Kind != "pulls" && req.Kind != "all" {
			return reviewRequest{}, fmt.Errorf(`kind must be "issues", "pulls", or "all"`)
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 20)
		return req, err
	case ActionListIssues:
		if err := githubcommon.RejectUnexpectedFields(action, present, "state", "labels", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 20)
		return req, err
	case ActionGetIssue:
		if err := githubcommon.RejectUnexpectedFields(action, present, "issue_number"); err != nil {
			return reviewRequest{}, err
		}
		if req.IssueNumber <= 0 {
			return reviewRequest{}, fmt.Errorf("issue_number must be a positive integer")
		}
		return req, nil
	case ActionListIssueComments:
		if err := githubcommon.RejectUnexpectedFields(action, present, "issue_number", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		if req.IssueNumber <= 0 {
			return reviewRequest{}, fmt.Errorf("issue_number must be a positive integer")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 50)
		return req, err
	case ActionGetIssueTimeline:
		if err := githubcommon.RejectUnexpectedFields(action, present, "issue_number", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		if req.IssueNumber <= 0 {
			return reviewRequest{}, fmt.Errorf("issue_number must be a positive integer")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 100)
		return req, err
	case ActionListPullRequests:
		if err := githubcommon.RejectUnexpectedFields(action, present, "state", "base", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 20)
		return req, err
	case ActionGetPullRequest:
		if err := githubcommon.RejectUnexpectedFields(action, present, "pull_number"); err != nil {
			return reviewRequest{}, err
		}
		if req.PullNumber <= 0 {
			return reviewRequest{}, fmt.Errorf("pull_number must be a positive integer")
		}
		return req, nil
	case ActionListPullRequestFiles, ActionListPullRequestCommits, ActionListPullRequestReviews, ActionListPullRequestReviewComments:
		if err := githubcommon.RejectUnexpectedFields(action, present, "pull_number", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		if req.PullNumber <= 0 {
			return reviewRequest{}, fmt.Errorf("pull_number must be a positive integer")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 100)
		return req, err
	case ActionListCommits:
		if err := githubcommon.RejectUnexpectedFields(action, present, "sha", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 20)
		return req, err
	case ActionGetCommit:
		if err := githubcommon.RejectUnexpectedFields(action, present, "ref", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		if req.Ref == "" {
			return reviewRequest{}, fmt.Errorf("ref must not be empty")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 100)
		return req, err
	case ActionListCommitsForPath:
		if err := githubcommon.RejectUnexpectedFields(action, present, "path", "sha", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		if req.Path == "" {
			return reviewRequest{}, fmt.Errorf("path must not be empty")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 20)
		return req, err
	case ActionCompareRefs:
		if err := githubcommon.RejectUnexpectedFields(action, present, "base", "head", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		if req.Base == "" {
			return reviewRequest{}, fmt.Errorf("base must not be empty")
		}
		if req.Head == "" {
			return reviewRequest{}, fmt.Errorf("head must not be empty")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 100)
		return req, err
	case ActionListPullRequestsForCommit:
		if err := githubcommon.RejectUnexpectedFields(action, present, "commit_sha", "page", "per_page"); err != nil {
			return reviewRequest{}, err
		}
		if req.CommitSHA == "" {
			return reviewRequest{}, fmt.Errorf("commit_sha must not be empty")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 30)
		return req, err
	default:
		return reviewRequest{}, fmt.Errorf("unsupported action %q", action)
	}
}

func executeRead(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	switch req.Action {
	case ActionSearchIssues:
		return executeSearchIssues(ctx, client, req)
	case ActionListIssues:
		return executeListIssues(ctx, client, req)
	case ActionGetIssue:
		return executeGetIssue(ctx, client, req)
	case ActionListIssueComments:
		return executeListIssueComments(ctx, client, req)
	case ActionGetIssueTimeline:
		return executeGetIssueTimeline(ctx, client, req)
	case ActionListPullRequests:
		return executeListPullRequests(ctx, client, req)
	case ActionGetPullRequest:
		return executeGetPullRequest(ctx, client, req)
	case ActionListPullRequestFiles:
		return executeListPullRequestFiles(ctx, client, req)
	case ActionListPullRequestCommits:
		return executeListPullRequestCommits(ctx, client, req)
	case ActionListPullRequestReviews:
		return executeListPullRequestReviews(ctx, client, req)
	case ActionListPullRequestReviewComments:
		return executeListPullRequestReviewComments(ctx, client, req)
	case ActionListCommits:
		return executeListCommits(ctx, client, req)
	case ActionGetCommit:
		return executeGetCommit(ctx, client, req)
	case ActionListCommitsForPath:
		return executeListCommitsForPath(ctx, client, req)
	case ActionCompareRefs:
		return executeCompareRefs(ctx, client, req)
	case ActionListPullRequestsForCommit:
		return executeListPullRequestsForCommit(ctx, client, req)
	default:
		return githubcommon.ToolResponse{}, fmt.Errorf("unsupported action %q", req.Action)
	}
}

func executeSearchIssues(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	searchQuery := strings.TrimSpace(req.Query)
	if !strings.Contains(strings.ToLower(searchQuery), "repo:") {
		searchQuery = strings.TrimSpace(searchQuery + " repo:" + req.Repo.Owner + "/" + req.Repo.Repo)
	}
	switch req.Kind {
	case "issues":
		searchQuery = strings.TrimSpace(searchQuery + " is:issue")
	case "pulls":
		searchQuery = strings.TrimSpace(searchQuery + " is:pr")
	}
	query := url.Values{}
	query.Set("q", searchQuery)
	query.Set("page", fmt.Sprintf("%d", req.Pagination.Page))
	query.Set("per_page", fmt.Sprintf("%d", req.Pagination.PerPage))

	var response struct {
		TotalCount        int  `json:"total_count"`
		IncompleteResults bool `json:"incomplete_results"`
		Items             []struct {
			Number      int64       `json:"number"`
			Title       string      `json:"title"`
			State       string      `json:"state"`
			Labels      []labelName `json:"labels"`
			PullRequest *struct{}   `json:"pull_request"`
		} `json:"items"`
	}
	meta, err := client.JSON(ctx, "GET", "/search/issues", query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	items := make([]map[string]any, 0, len(response.Items))
	for _, item := range response.Items {
		items = append(items, map[string]any{
			"number": item.Number,
			"kind":   issueKind(item.PullRequest != nil),
			"title":  strings.TrimSpace(item.Title),
			"state":  strings.TrimSpace(item.State),
			"labels": labelNames(item.Labels),
		})
	}
	var warnings []string
	if response.IncompleteResults {
		warnings = append(warnings, "GitHub returned incomplete_results=true for this issue search")
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"query":              searchQuery,
		"total_count":        response.TotalCount,
		"incomplete_results": response.IncompleteResults,
		"items":              items,
	}, warnings, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListIssues(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := url.Values{}
	if req.State != "" {
		query.Set("state", req.State)
	}
	if len(req.Labels) > 0 {
		query.Set("labels", strings.Join(req.Labels, ","))
	}
	query.Set("page", fmt.Sprintf("%d", req.Pagination.Page))
	query.Set("per_page", fmt.Sprintf("%d", req.Pagination.PerPage))

	var response []struct {
		Number      int64       `json:"number"`
		Title       string      `json:"title"`
		State       string      `json:"state"`
		Labels      []labelName `json:"labels"`
		Assignees   []userLogin `json:"assignees"`
		PullRequest *struct{}   `json:"pull_request"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, "/issues"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	items := make([]map[string]any, 0, len(response))
	for _, item := range response {
		items = append(items, map[string]any{
			"number":          item.Number,
			"title":           strings.TrimSpace(item.Title),
			"state":           strings.TrimSpace(item.State),
			"labels":          labelNames(item.Labels),
			"assignees":       userLogins(item.Assignees),
			"is_pull_request": item.PullRequest != nil,
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"issues": items,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetIssue(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		Number    int64       `json:"number"`
		Title     string      `json:"title"`
		State     string      `json:"state"`
		Body      string      `json:"body"`
		Labels    []labelName `json:"labels"`
		Assignees []userLogin `json:"assignees"`
		Milestone *struct {
			Title string `json:"title"`
		} `json:"milestone"`
		PullRequest *struct{} `json:"pull_request"`
	}
	_, err := client.JSON(ctx, "GET", reviewPath(req.Repo, fmt.Sprintf("/issues/%d", req.IssueNumber)), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	data := map[string]any{
		"number":          response.Number,
		"title":           strings.TrimSpace(response.Title),
		"state":           strings.TrimSpace(response.State),
		"body":            response.Body,
		"labels":          labelNames(response.Labels),
		"assignees":       userLogins(response.Assignees),
		"is_pull_request": response.PullRequest != nil,
	}
	if response.Milestone != nil {
		data["milestone"] = strings.TrimSpace(response.Milestone.Title)
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, data, nil, nil, nil), nil
}

func executeListIssueComments(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response []struct {
		ID        int64  `json:"id"`
		CreatedAt string `json:"created_at"`
		Body      string `json:"body"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, fmt.Sprintf("/issues/%d/comments", req.IssueNumber)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	comments := make([]map[string]any, 0, len(response))
	for _, comment := range response {
		comments = append(comments, map[string]any{
			"id":         comment.ID,
			"user":       strings.TrimSpace(comment.User.Login),
			"created_at": strings.TrimSpace(comment.CreatedAt),
			"body":       comment.Body,
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"issue_number": req.IssueNumber,
		"comments":     comments,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetIssueTimeline(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response []struct {
		Event     string `json:"event"`
		CreatedAt string `json:"created_at"`
		CommitID  string `json:"commit_id"`
		Actor     *struct {
			Login string `json:"login"`
		} `json:"actor"`
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, fmt.Sprintf("/issues/%d/timeline", req.IssueNumber)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	events := make([]map[string]any, 0, len(response))
	for _, event := range response {
		item := map[string]any{
			"event":      strings.TrimSpace(event.Event),
			"created_at": strings.TrimSpace(event.CreatedAt),
			"commit_id":  strings.TrimSpace(event.CommitID),
		}
		if event.Actor != nil {
			item["actor"] = strings.TrimSpace(event.Actor.Login)
		} else if event.User != nil {
			item["actor"] = strings.TrimSpace(event.User.Login)
		}
		events = append(events, item)
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"issue_number": req.IssueNumber,
		"events":       events,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListPullRequests(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	if req.State != "" {
		query.Set("state", req.State)
	}
	if req.Base != "" {
		query.Set("base", req.Base)
	}
	var response []struct {
		Number             int64       `json:"number"`
		Title              string      `json:"title"`
		State              string      `json:"state"`
		Draft              bool        `json:"draft"`
		RequestedReviewers []userLogin `json:"requested_reviewers"`
		RequestedTeams     []teamSlug  `json:"requested_teams"`
		Head               struct {
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, "/pulls"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	items := make([]map[string]any, 0, len(response))
	for _, pr := range response {
		items = append(items, map[string]any{
			"number":              pr.Number,
			"title":               strings.TrimSpace(pr.Title),
			"state":               strings.TrimSpace(pr.State),
			"draft":               pr.Draft,
			"head_ref":            strings.TrimSpace(pr.Head.Ref),
			"base_ref":            strings.TrimSpace(pr.Base.Ref),
			"requested_reviewers": userLogins(pr.RequestedReviewers),
			"requested_teams":     teamSlugs(pr.RequestedTeams),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"pull_requests": items,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetPullRequest(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		Number             int64       `json:"number"`
		Title              string      `json:"title"`
		State              string      `json:"state"`
		Draft              bool        `json:"draft"`
		MergeableState     string      `json:"mergeable_state"`
		RequestedReviewers []userLogin `json:"requested_reviewers"`
		RequestedTeams     []teamSlug  `json:"requested_teams"`
		Head               struct {
			Sha string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	_, err := client.JSON(ctx, "GET", reviewPath(req.Repo, fmt.Sprintf("/pulls/%d", req.PullNumber)), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"number":              response.Number,
		"title":               strings.TrimSpace(response.Title),
		"state":               strings.TrimSpace(response.State),
		"draft":               response.Draft,
		"mergeable_state":     strings.TrimSpace(response.MergeableState),
		"head_sha":            strings.TrimSpace(response.Head.Sha),
		"head_ref":            strings.TrimSpace(response.Head.Ref),
		"base_ref":            strings.TrimSpace(response.Base.Ref),
		"requested_reviewers": userLogins(response.RequestedReviewers),
		"requested_teams":     teamSlugs(response.RequestedTeams),
	}, nil, nil, nil), nil
}

func executeListPullRequestFiles(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response []struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Changes   int    `json:"changes"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, fmt.Sprintf("/pulls/%d/files", req.PullNumber)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	files := make([]map[string]any, 0, len(response))
	for _, file := range response {
		files = append(files, map[string]any{
			"path":      strings.TrimSpace(file.Filename),
			"status":    strings.TrimSpace(file.Status),
			"additions": file.Additions,
			"deletions": file.Deletions,
			"changes":   file.Changes,
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"pull_number": req.PullNumber,
		"files":       files,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListPullRequestCommits(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response []struct {
		Sha    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, fmt.Sprintf("/pulls/%d/commits", req.PullNumber)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	commits := make([]map[string]any, 0, len(response))
	for _, commit := range response {
		commits = append(commits, map[string]any{
			"sha":   strings.TrimSpace(commit.Sha),
			"title": firstLine(commit.Commit.Message),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"pull_number": req.PullNumber,
		"commits":     commits,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListPullRequestReviews(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response []struct {
		ID          int64  `json:"id"`
		State       string `json:"state"`
		SubmittedAt string `json:"submitted_at"`
		Body        string `json:"body"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, fmt.Sprintf("/pulls/%d/reviews", req.PullNumber)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	reviews := make([]map[string]any, 0, len(response))
	for _, review := range response {
		reviews = append(reviews, map[string]any{
			"id":           review.ID,
			"user":         strings.TrimSpace(review.User.Login),
			"state":        strings.TrimSpace(review.State),
			"submitted_at": strings.TrimSpace(review.SubmittedAt),
			"body":         review.Body,
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"pull_number": req.PullNumber,
		"reviews":     reviews,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListPullRequestReviewComments(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response []struct {
		ID        int64  `json:"id"`
		Path      string `json:"path"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		User      struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, fmt.Sprintf("/pulls/%d/comments", req.PullNumber)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	comments := make([]map[string]any, 0, len(response))
	for _, comment := range response {
		comments = append(comments, map[string]any{
			"id":         comment.ID,
			"path":       strings.TrimSpace(comment.Path),
			"user":       strings.TrimSpace(comment.User.Login),
			"body":       comment.Body,
			"created_at": strings.TrimSpace(comment.CreatedAt),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"pull_number": req.PullNumber,
		"comments":    comments,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListCommits(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	if req.Sha != "" {
		query.Set("sha", req.Sha)
	}
	var response []struct {
		Sha    string `json:"sha"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, "/commits"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	commits := make([]map[string]any, 0, len(response))
	for _, commit := range response {
		author := ""
		if commit.Author != nil {
			author = strings.TrimSpace(commit.Author.Login)
		}
		if author == "" {
			author = strings.TrimSpace(commit.Commit.Author.Name)
		}
		commits = append(commits, map[string]any{
			"sha":          strings.TrimSpace(commit.Sha),
			"title":        firstLine(commit.Commit.Message),
			"author":       author,
			"committed_at": strings.TrimSpace(commit.Commit.Author.Date),
		})
	}
	data := map[string]any{
		"commits": commits,
	}
	if req.Sha != "" {
		data["ref"] = req.Sha
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, data, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetCommit(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response struct {
		Sha    string `json:"sha"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
		Files []struct {
			Filename string `json:"filename"`
			Status   string `json:"status"`
			Changes  int    `json:"changes"`
		} `json:"files"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, "/commits/"+url.PathEscape(req.Ref)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	files := make([]map[string]any, 0, len(response.Files))
	for _, file := range response.Files {
		files = append(files, map[string]any{
			"path":    strings.TrimSpace(file.Filename),
			"status":  strings.TrimSpace(file.Status),
			"changes": file.Changes,
		})
	}
	author := ""
	if response.Author != nil {
		author = strings.TrimSpace(response.Author.Login)
	}
	if author == "" {
		author = strings.TrimSpace(response.Commit.Author.Name)
	}
	data := map[string]any{
		"sha":          strings.TrimSpace(response.Sha),
		"title":        firstLine(response.Commit.Message),
		"author":       author,
		"committed_at": strings.TrimSpace(response.Commit.Author.Date),
		"files":        files,
	}
	pageInfo := githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link"))
	var warnings []string
	if pageInfo != nil && pageInfo.HasNextPage {
		warnings = append(warnings, "Commit file list is paginated; request the next page to continue")
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, data, warnings, pageInfo, nil), nil
}

func executeListCommitsForPath(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	query.Set("path", req.Path)
	if req.Sha != "" {
		query.Set("sha", req.Sha)
	}
	var response []struct {
		Sha    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, "/commits"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	commits := make([]map[string]any, 0, len(response))
	for _, commit := range response {
		commits = append(commits, map[string]any{
			"sha":   strings.TrimSpace(commit.Sha),
			"title": firstLine(commit.Commit.Message),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"path":    req.Path,
		"commits": commits,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeCompareRefs(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response struct {
		Status   string `json:"status"`
		AheadBy  int    `json:"ahead_by"`
		BehindBy int    `json:"behind_by"`
		Files    []struct {
			Filename string `json:"filename"`
			Status   string `json:"status"`
			Changes  int    `json:"changes"`
		} `json:"files"`
	}
	baseHead := url.PathEscape(req.Base + "..." + req.Head)
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, "/compare/"+baseHead), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	files := make([]map[string]any, 0, len(response.Files))
	for _, file := range response.Files {
		files = append(files, map[string]any{
			"path":    strings.TrimSpace(file.Filename),
			"status":  strings.TrimSpace(file.Status),
			"changes": file.Changes,
		})
	}
	pageInfo := githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link"))
	var warnings []string
	if pageInfo != nil && pageInfo.HasNextPage {
		warnings = append(warnings, "Compare results are paginated; request the next page to continue")
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"base":      req.Base,
		"head":      req.Head,
		"status":    strings.TrimSpace(response.Status),
		"ahead_by":  response.AheadBy,
		"behind_by": response.BehindBy,
		"files":     files,
	}, warnings, pageInfo, nil), nil
}

func executeListPullRequestsForCommit(ctx context.Context, client *githubcommon.Client, req reviewRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response []struct {
		Number int64  `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
	}
	meta, err := client.JSON(ctx, "GET", reviewPath(req.Repo, "/commits/"+url.PathEscape(req.CommitSHA)+"/pulls"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	pulls := make([]map[string]any, 0, len(response))
	for _, pr := range response {
		pulls = append(pulls, map[string]any{
			"number": pr.Number,
			"title":  strings.TrimSpace(pr.Title),
			"state":  strings.TrimSpace(pr.State),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"commit_sha":    req.CommitSHA,
		"pull_requests": pulls,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func reviewPath(repo githubcommon.RepoCoordinates, suffix string) string {
	return fmt.Sprintf("/repos/%s/%s%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Repo), suffix)
}

func paginationQuery(p githubcommon.Pagination) url.Values {
	query := url.Values{}
	query.Set("page", fmt.Sprintf("%d", p.Page))
	query.Set("per_page", fmt.Sprintf("%d", p.PerPage))
	return query
}

func labelNames(labels []labelName) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		names = append(names, strings.TrimSpace(label.Name))
	}
	return githubcommon.NormalizeStringSlice(names)
}

func userLogins(users []userLogin) []string {
	logins := make([]string, 0, len(users))
	for _, user := range users {
		logins = append(logins, strings.TrimSpace(user.Login))
	}
	return githubcommon.NormalizeStringSlice(logins)
}

func teamSlugs(teams []teamSlug) []string {
	slugs := make([]string, 0, len(teams))
	for _, team := range teams {
		slugs = append(slugs, strings.TrimSpace(team.Slug))
	}
	return githubcommon.NormalizeStringSlice(slugs)
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	return strings.TrimSpace(lines[0])
}

func issueKind(isPullRequest bool) string {
	if isPullRequest {
		return "pull_request"
	}
	return "issue"
}
