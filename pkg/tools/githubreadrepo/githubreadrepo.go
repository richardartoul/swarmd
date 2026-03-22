package githubreadrepo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/richardartoul/swarmd/pkg/server"
	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
	"github.com/richardartoul/swarmd/pkg/tools/githubcommon"
)

var registerOnce sync.Once

type plugin struct {
	host server.ToolHost
}

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		server.RegisterTool(func(host server.ToolHost) toolscore.ToolPlugin {
			return plugin{host: host}
		}, server.ToolRegistrationOptions{
			RequiredEnv:   []string{githubcommon.GitHubTokenEnvVar},
			RequiredHosts: githubcommon.RequiredHosts(),
		})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        ToolName,
		Description: "Read GitHub repository metadata, tree and file contents, code search results, branches, and branch protection rules through a server-owned GitHub client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"action": toolscommon.StringEnumSchema(
					"GitHub repository read action to execute.",
					ActionGetRepo,
					ActionSearchCode,
					ActionListTree,
					ActionGetFileContents,
					ActionListBranches,
					ActionGetBranch,
					ActionGetRulesForBranch,
					ActionListRulesets,
					ActionGetRuleset,
				),
				"owner": toolscommon.StringSchema("GitHub repository owner or organization."),
				"repo":  toolscommon.StringSchema("GitHub repository name."),
				"query": toolscommon.StringSchema(`Required for "search_code". The tool automatically scopes the search to the repository.`),
				"ref":   toolscommon.StringSchema(`Git ref for "list_tree" or optional ref override for "get_file_contents".`),
				"path":  toolscommon.StringSchema(`Repository file path for "get_file_contents".`),
				"branch": toolscommon.StringSchema(
					`Branch name for "get_branch" or "get_rules_for_branch".`,
				),
				"page":     toolscommon.IntegerSchema(`Optional page number for paginated list and search actions.`),
				"per_page": toolscommon.IntegerSchema(`Optional page size for paginated list and search actions. Values above 100 are capped.`),
				"recursive": toolscommon.BooleanSchema(
					`Whether "list_tree" should recurse into subdirectories.`,
				),
				"protected": toolscommon.BooleanSchema(
					`Optional protected branch filter for "list_branches".`,
				),
				"includes_parents": toolscommon.BooleanSchema(
					`Optional parent ruleset inclusion flag for "list_rulesets".`,
				),
				"targets": githubcommon.StringArraySchema(
					`Optional ruleset targets for "list_rulesets", such as ["branch"].`,
				),
				"ruleset_id": toolscommon.IntegerSchema(`Ruleset ID for "get_ruleset".`),
			},
			"action",
			"owner",
			"repo",
		),
		RequiredArguments: []string{"action", "owner", "repo"},
		Examples: []string{
			`{"action":"get_repo","owner":"acme","repo":"monorepo"}`,
			`{"action":"search_code","owner":"acme","repo":"monorepo","query":"FlakyTest path:services/payments language:go","page":1,"per_page":25}`,
			`{"action":"get_file_contents","owner":"acme","repo":"monorepo","path":"services/payments/flaky_test.go","ref":"main"}`,
		},
		OutputNotes:  "Returns normalized JSON describing repository discovery and branch-policy state.",
		SafetyTags:   []string{"network", "read_only"},
		NetworkScope: toolscore.ToolNetworkScopeScoped,
		ReadOnly:     true,
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

func normalizeReadRequest(input input) (repoRequest, error) {
	coords, err := githubcommon.NormalizeRepoCoordinates(input.Owner, input.Repo)
	if err != nil {
		return repoRequest{}, err
	}
	action := strings.TrimSpace(input.Action)
	if action == "" {
		return repoRequest{}, fmt.Errorf("action must not be empty")
	}
	req := repoRequest{
		Action:          action,
		Repo:            coords,
		Query:           strings.TrimSpace(input.Query),
		Ref:             strings.TrimSpace(input.Ref),
		Branch:          strings.TrimSpace(input.Branch),
		Protected:       input.Protected,
		IncludesParents: input.IncludesParents,
		Targets:         githubcommon.NormalizeStringSlice(input.Targets),
		RulesetID:       input.RulesetID,
	}
	if input.Recursive != nil {
		req.Recursive = *input.Recursive
	}
	if strings.TrimSpace(input.Path) != "" {
		req.Path, err = githubcommon.NormalizeRepoPath(input.Path)
		if err != nil {
			return repoRequest{}, err
		}
	}
	present := map[string]bool{
		"query":            req.Query != "",
		"ref":              req.Ref != "",
		"path":             req.Path != "",
		"branch":           req.Branch != "",
		"page":             input.Page != 0,
		"per_page":         input.PerPage != 0,
		"recursive":        input.Recursive != nil,
		"protected":        input.Protected != nil,
		"includes_parents": input.IncludesParents != nil,
		"targets":          len(req.Targets) > 0,
		"ruleset_id":       req.RulesetID != 0,
	}
	switch action {
	case ActionGetRepo:
		return req, githubcommon.RejectUnexpectedFields(action, present)
	case ActionSearchCode:
		if err := githubcommon.RejectUnexpectedFields(action, present, "query", "page", "per_page"); err != nil {
			return repoRequest{}, err
		}
		if req.Query == "" {
			return repoRequest{}, fmt.Errorf("query must not be empty")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 25)
		return req, err
	case ActionListTree:
		if err := githubcommon.RejectUnexpectedFields(action, present, "ref", "recursive"); err != nil {
			return repoRequest{}, err
		}
		if req.Ref == "" {
			return repoRequest{}, fmt.Errorf("ref must not be empty")
		}
		return req, nil
	case ActionGetFileContents:
		if err := githubcommon.RejectUnexpectedFields(action, present, "path", "ref"); err != nil {
			return repoRequest{}, err
		}
		if req.Path == "" {
			return repoRequest{}, fmt.Errorf("path must not be empty")
		}
		return req, nil
	case ActionListBranches:
		if err := githubcommon.RejectUnexpectedFields(action, present, "protected", "page", "per_page"); err != nil {
			return repoRequest{}, err
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 30)
		return req, err
	case ActionGetBranch, ActionGetRulesForBranch:
		if err := githubcommon.RejectUnexpectedFields(action, present, "branch"); err != nil {
			return repoRequest{}, err
		}
		if req.Branch == "" {
			return repoRequest{}, fmt.Errorf("branch must not be empty")
		}
		return req, nil
	case ActionListRulesets:
		if err := githubcommon.RejectUnexpectedFields(action, present, "includes_parents", "targets", "page", "per_page"); err != nil {
			return repoRequest{}, err
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 30)
		return req, err
	case ActionGetRuleset:
		if err := githubcommon.RejectUnexpectedFields(action, present, "ruleset_id"); err != nil {
			return repoRequest{}, err
		}
		if req.RulesetID <= 0 {
			return repoRequest{}, fmt.Errorf("ruleset_id must be a positive integer")
		}
		return req, nil
	default:
		return repoRequest{}, fmt.Errorf("unsupported action %q", action)
	}
}

func executeRead(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	switch req.Action {
	case ActionGetRepo:
		return executeGetRepo(ctx, client, req)
	case ActionSearchCode:
		return executeSearchCode(ctx, client, req)
	case ActionListTree:
		return executeListTree(ctx, client, req)
	case ActionGetFileContents:
		return executeGetFileContents(ctx, client, req)
	case ActionListBranches:
		return executeListBranches(ctx, client, req)
	case ActionGetBranch:
		return executeGetBranch(ctx, client, req)
	case ActionGetRulesForBranch:
		return executeGetRulesForBranch(ctx, client, req)
	case ActionListRulesets:
		return executeListRulesets(ctx, client, req)
	case ActionGetRuleset:
		return executeGetRuleset(ctx, client, req)
	default:
		return githubcommon.ToolResponse{}, fmt.Errorf("unsupported action %q", req.Action)
	}
}

func executeGetRepo(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		FullName      string   `json:"full_name"`
		DefaultBranch string   `json:"default_branch"`
		Visibility    string   `json:"visibility"`
		Archived      bool     `json:"archived"`
		Topics        []string `json:"topics"`
		License       *struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"license"`
	}
	_, err := client.JSON(ctx, "GET", repoPath(req.Repo, ""), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	data := map[string]any{
		"full_name":      strings.TrimSpace(response.FullName),
		"default_branch": strings.TrimSpace(response.DefaultBranch),
		"visibility":     strings.TrimSpace(response.Visibility),
		"archived":       response.Archived,
		"topics":         githubcommon.NormalizeStringSlice(response.Topics),
	}
	if response.License != nil {
		data["license"] = map[string]any{
			"key":  strings.TrimSpace(response.License.Key),
			"name": strings.TrimSpace(response.License.Name),
		}
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, data, nil, nil, nil), nil
}

func executeSearchCode(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	searchQuery := strings.TrimSpace(req.Query)
	if !strings.Contains(strings.ToLower(searchQuery), "repo:") {
		searchQuery = strings.TrimSpace(searchQuery + " repo:" + req.Repo.Owner + "/" + req.Repo.Repo)
	}
	query := url.Values{}
	query.Set("q", searchQuery)
	query.Set("page", fmt.Sprintf("%d", req.Pagination.Page))
	query.Set("per_page", fmt.Sprintf("%d", req.Pagination.PerPage))

	var response struct {
		TotalCount        int  `json:"total_count"`
		IncompleteResults bool `json:"incomplete_results"`
		Items             []struct {
			Path       string `json:"path"`
			Sha        string `json:"sha"`
			HTMLURL    string `json:"html_url"`
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"items"`
	}
	meta, err := client.JSON(ctx, "GET", "/search/code", query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	items := make([]map[string]any, 0, len(response.Items))
	for _, item := range response.Items {
		items = append(items, map[string]any{
			"repository": strings.TrimSpace(item.Repository.FullName),
			"path":       strings.TrimSpace(item.Path),
			"sha":        strings.TrimSpace(item.Sha),
			"html_url":   strings.TrimSpace(item.HTMLURL),
		})
	}
	warnings := []string{"GitHub code search only indexes the default branch"}
	if response.IncompleteResults {
		warnings = append(warnings, "GitHub returned incomplete_results=true for this code search")
	}
	pageInfo := githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link"))
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"query":              searchQuery,
		"total_count":        response.TotalCount,
		"incomplete_results": response.IncompleteResults,
		"items":              items,
	}, warnings, pageInfo, nil), nil
}

func executeListTree(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	query := url.Values{}
	if req.Recursive {
		query.Set("recursive", "1")
	}
	var response struct {
		Sha       string `json:"sha"`
		Truncated bool   `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Type string `json:"type"`
			Size int    `json:"size"`
			Sha  string `json:"sha"`
		} `json:"tree"`
	}
	meta, err := client.JSON(ctx, "GET", repoPath(req.Repo, "/git/trees/"+url.PathEscape(req.Ref)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	_ = meta
	entries := make([]map[string]any, 0, len(response.Tree))
	for _, item := range response.Tree {
		entry := map[string]any{
			"path": strings.TrimSpace(item.Path),
			"type": strings.TrimSpace(item.Type),
		}
		if strings.TrimSpace(item.Type) == "blob" {
			entry["size"] = item.Size
		}
		if strings.TrimSpace(item.Sha) != "" {
			entry["sha"] = strings.TrimSpace(item.Sha)
		}
		entries = append(entries, entry)
	}
	var warnings []string
	if response.Truncated {
		warnings = append(warnings, "Git tree listing was truncated by GitHub")
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"ref":       req.Ref,
		"tree_sha":  strings.TrimSpace(response.Sha),
		"truncated": response.Truncated,
		"entries":   entries,
	}, warnings, nil, nil), nil
}

func executeGetFileContents(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	query := url.Values{}
	if req.Ref != "" {
		query.Set("ref", req.Ref)
	}
	var raw json.RawMessage
	_, err := client.JSON(ctx, "GET", repoPath(req.Repo, "/contents/"+githubcommon.EscapePathPreservingSlashes(req.Path)), query, "", &raw)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	if len(raw) > 0 && raw[0] == '[' {
		return githubcommon.FailureResponse(ToolName, req.Action, &githubcommon.APIError{
			Code:    "not_a_file",
			Message: fmt.Sprintf("%s is not a file path", req.Path),
		}, nil, nil, nil), nil
	}
	var response struct {
		Type            string  `json:"type"`
		Path            string  `json:"path"`
		Sha             string  `json:"sha"`
		Size            int     `json:"size"`
		Encoding        string  `json:"encoding"`
		Content         *string `json:"content"`
		DownloadURL     string  `json:"download_url"`
		Target          string  `json:"target"`
		SubmoduleGitURL string  `json:"submodule_git_url"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return githubcommon.ToolResponse{}, fmt.Errorf("decode file contents response: %w", err)
	}
	entryType := strings.TrimSpace(response.Type)
	data := map[string]any{
		"path":       toolscommon.FirstNonEmptyString(strings.TrimSpace(response.Path), req.Path),
		"ref":        req.Ref,
		"sha":        strings.TrimSpace(response.Sha),
		"size_bytes": response.Size,
	}
	if entryType != "" {
		data["type"] = entryType
	}
	if downloadURL := strings.TrimSpace(response.DownloadURL); downloadURL != "" {
		data["download_url"] = downloadURL
	}
	if target := strings.TrimSpace(response.Target); target != "" {
		data["target"] = target
	}
	if submoduleGitURL := strings.TrimSpace(response.SubmoduleGitURL); submoduleGitURL != "" {
		data["submodule_git_url"] = submoduleGitURL
	}
	var warnings []string
	encoding := strings.TrimSpace(response.Encoding)
	content := ""
	if response.Content != nil {
		content = *response.Content
	}
	trimmedContent := strings.TrimSpace(content)
	if strings.EqualFold(encoding, "base64") && trimmedContent != "" {
		decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(trimmedContent, "\n", ""))
		if err != nil {
			return githubcommon.ToolResponse{}, fmt.Errorf("decode file content: %w", err)
		}
		if utf8.Valid(decoded) {
			data["encoding"] = "utf-8"
			data["content_inline"] = string(decoded)
		} else {
			data["encoding"] = "base64"
			data["content_base64"] = strings.ReplaceAll(trimmedContent, "\n", "")
			warnings = append(warnings, "File contents are not valid UTF-8; returning base64 content instead of content_inline")
		}
	} else if trimmedContent != "" {
		if encoding != "" {
			data["encoding"] = encoding
		}
		if content != "" {
			data["content_inline"] = content
		}
	} else {
		if encoding != "" {
			data["encoding"] = encoding
		}
		switch entryType {
		case "symlink":
			warnings = append(warnings, "GitHub returned a symlink entry without inline file content")
		case "submodule":
			warnings = append(warnings, "GitHub returned a submodule entry without inline file content")
		default:
			if response.Size > 0 || strings.TrimSpace(response.DownloadURL) != "" {
				warnings = append(warnings, "GitHub did not include inline file content for this path")
			}
		}
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, data, warnings, nil, nil), nil
}

func executeListBranches(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	query := url.Values{}
	query.Set("page", fmt.Sprintf("%d", req.Pagination.Page))
	query.Set("per_page", fmt.Sprintf("%d", req.Pagination.PerPage))
	if req.Protected != nil {
		query.Set("protected", fmt.Sprintf("%t", *req.Protected))
	}
	var response []struct {
		Name          string `json:"name"`
		Protected     bool   `json:"protected"`
		ProtectionURL string `json:"protection_url"`
		Commit        struct {
			Sha string `json:"sha"`
		} `json:"commit"`
	}
	meta, err := client.JSON(ctx, "GET", repoPath(req.Repo, "/branches"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	branches := make([]map[string]any, 0, len(response))
	for _, branch := range response {
		branches = append(branches, map[string]any{
			"name":           strings.TrimSpace(branch.Name),
			"protected":      branch.Protected,
			"head_sha":       strings.TrimSpace(branch.Commit.Sha),
			"protection_url": strings.TrimSpace(branch.ProtectionURL),
		})
	}
	pageInfo := githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link"))
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"branches": branches,
	}, nil, pageInfo, nil), nil
}

func executeGetBranch(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		Name          string `json:"name"`
		Protected     bool   `json:"protected"`
		ProtectionURL string `json:"protection_url"`
		Commit        struct {
			Sha string `json:"sha"`
		} `json:"commit"`
		Protection *struct {
			RequiredStatusChecks *struct {
				Strict   bool     `json:"strict"`
				Contexts []string `json:"contexts"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	_, err := client.JSON(ctx, "GET", repoPath(req.Repo, "/branches/"+url.PathEscape(req.Branch)), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	data := map[string]any{
		"name":           strings.TrimSpace(response.Name),
		"protected":      response.Protected,
		"head_sha":       strings.TrimSpace(response.Commit.Sha),
		"protection_url": strings.TrimSpace(response.ProtectionURL),
	}
	if response.Protection != nil && response.Protection.RequiredStatusChecks != nil {
		data["required_status_checks"] = map[string]any{
			"strict":   response.Protection.RequiredStatusChecks.Strict,
			"contexts": githubcommon.NormalizeStringSlice(response.Protection.RequiredStatusChecks.Contexts),
		}
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, data, nil, nil, nil), nil
}

func executeGetRulesForBranch(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	var payload any
	_, err := client.JSON(ctx, "GET", repoPath(req.Repo, "/rules/branches/"+url.PathEscape(req.Branch)), nil, "", &payload)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"branch": req.Branch,
		"rules":  extractRules(payload),
	}, nil, nil, nil), nil
}

func executeListRulesets(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	query := url.Values{}
	if req.IncludesParents != nil {
		query.Set("includes_parents", fmt.Sprintf("%t", *req.IncludesParents))
	}
	if len(req.Targets) > 0 {
		query.Set("targets", strings.Join(req.Targets, ","))
	}
	query.Set("page", fmt.Sprintf("%d", req.Pagination.Page))
	query.Set("per_page", fmt.Sprintf("%d", req.Pagination.PerPage))
	var response []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Target      string `json:"target"`
		Enforcement string `json:"enforcement"`
	}
	meta, err := client.JSON(ctx, "GET", repoPath(req.Repo, "/rulesets"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	rulesets := make([]map[string]any, 0, len(response))
	for _, ruleset := range response {
		rulesets = append(rulesets, map[string]any{
			"id":          ruleset.ID,
			"name":        strings.TrimSpace(ruleset.Name),
			"target":      strings.TrimSpace(ruleset.Target),
			"enforcement": strings.TrimSpace(ruleset.Enforcement),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"rulesets": rulesets,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetRuleset(ctx context.Context, client *githubcommon.Client, req repoRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		ID           int64            `json:"id"`
		Name         string           `json:"name"`
		Target       string           `json:"target"`
		Enforcement  string           `json:"enforcement"`
		Conditions   map[string]any   `json:"conditions"`
		Rules        []map[string]any `json:"rules"`
		BypassActors []map[string]any `json:"bypass_actors"`
	}
	_, err := client.JSON(ctx, "GET", repoPath(req.Repo, fmt.Sprintf("/rulesets/%d", req.RulesetID)), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"id":            response.ID,
		"name":          strings.TrimSpace(response.Name),
		"target":        strings.TrimSpace(response.Target),
		"enforcement":   strings.TrimSpace(response.Enforcement),
		"conditions":    cleanJSONValue(response.Conditions),
		"rules":         cleanJSONValue(response.Rules),
		"bypass_actors": cleanJSONValue(response.BypassActors),
	}, nil, nil, nil), nil
}

func repoPath(repo githubcommon.RepoCoordinates, suffix string) string {
	return fmt.Sprintf("/repos/%s/%s%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Repo), suffix)
}

func extractRules(payload any) []any {
	switch typed := payload.(type) {
	case map[string]any:
		if rules, ok := typed["rules"]; ok {
			if values, ok := cleanJSONValue(rules).([]any); ok {
				return values
			}
		}
		return []any{cleanJSONValue(typed)}
	case []any:
		values := make([]any, 0, len(typed))
		for _, item := range typed {
			values = append(values, cleanJSONValue(item))
		}
		return values
	default:
		return []any{}
	}
}

func cleanJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(typed))
		for key, item := range typed {
			key = strings.TrimSpace(key)
			if key == "" || item == nil {
				continue
			}
			cleaned[key] = cleanJSONValue(item)
		}
		return cleaned
	case []any:
		values := make([]any, 0, len(typed))
		for _, item := range typed {
			if item == nil {
				continue
			}
			values = append(values, cleanJSONValue(item))
		}
		return values
	case []map[string]any:
		values := make([]any, 0, len(typed))
		for _, item := range typed {
			values = append(values, cleanJSONValue(item))
		}
		return values
	case string:
		return strings.TrimSpace(typed)
	default:
		return typed
	}
}
