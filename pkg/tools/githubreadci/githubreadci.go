package githubreadci

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
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

func init() {
	Register()
}

func Register() {
	registerOnce.Do(func() {
		server.RegisterTool(func(host server.ToolHost) toolscore.ToolPlugin {
			return plugin{host: host}
		}, server.ToolRegistrationOptions{
			RequiredEnv:   []string{githubcommon.GitHubTokenEnvVar},
			RequiredHosts: githubcommon.CIRequiredHosts(),
		})
	})
}

func (plugin) Definition() toolscore.ToolDefinition {
	return toolscore.ToolDefinition{
		Name:        ToolName,
		Description: "Read GitHub commit statuses, checks, workflows, workflow runs, jobs, logs, and artifacts through a server-owned GitHub client.",
		Kind:        toolscore.ToolKindFunction,
		Parameters: toolscommon.ObjectSchema(
			map[string]any{
				"action": toolscommon.StringEnumSchema(
					"GitHub CI read action to execute.",
					ActionGetCombinedStatus,
					ActionListCommitStatuses,
					ActionListCheckRuns,
					ActionGetCheckRun,
					ActionListCheckRunAnnotations,
					ActionListWorkflows,
					ActionGetWorkflow,
					ActionListWorkflowRuns,
					ActionListWorkflowRunsForWorkflow,
					ActionGetWorkflowRun,
					ActionGetWorkflowRunAttempt,
					ActionListWorkflowJobs,
					ActionListWorkflowJobsForAttempt,
					ActionGetWorkflowJob,
					ActionDownloadJobLogs,
					ActionDownloadWorkflowRunAttemptLogs,
					ActionDownloadWorkflowRunLogs,
					ActionListRunArtifacts,
					ActionGetArtifact,
					ActionDownloadArtifact,
				),
				"owner":          toolscommon.StringSchema("GitHub repository owner or organization."),
				"repo":           toolscommon.StringSchema("GitHub repository name."),
				"ref":            toolscommon.StringSchema(`Commit SHA or ref for status and check actions.`),
				"check_run_id":   toolscommon.IntegerSchema(`Check run ID for check run actions.`),
				"workflow_id":    toolscommon.StringSchema(`Workflow ID or filename for workflow actions.`),
				"run_id":         toolscommon.IntegerSchema(`Workflow run ID for run and job actions.`),
				"attempt_number": toolscommon.IntegerSchema(`Workflow run attempt number for attempt-aware actions.`),
				"job_id":         toolscommon.IntegerSchema(`Workflow job ID for job actions and log downloads.`),
				"filter": toolscommon.StringEnumSchema(
					`Optional filter for "list_workflow_jobs". Use "latest" or "all".`,
					"latest",
					"all",
				),
				"branch":   toolscommon.StringSchema(`Optional workflow run branch filter.`),
				"event":    toolscommon.StringSchema(`Optional workflow run event filter.`),
				"status":   toolscommon.StringSchema(`Optional workflow run status or conclusion filter.`),
				"created":  toolscommon.StringSchema(`Optional workflow run created filter, such as ">=2026-03-15".`),
				"head_sha": toolscommon.StringSchema(`Optional workflow run head SHA filter.`),
				"page":     toolscommon.IntegerSchema(`Optional page number for paginated actions.`),
				"per_page": toolscommon.IntegerSchema(`Optional page size for paginated actions. Values above 100 are capped.`),
				"artifact_id": toolscommon.IntegerSchema(
					`Artifact ID for artifact actions.`,
				),
				"extract": toolscommon.BooleanSchema(
					`Whether "download_artifact" should also extract the ZIP archive into the agent filesystem.`,
				),
			},
			"action",
			"owner",
			"repo",
		),
		RequiredArguments: []string{"action", "owner", "repo"},
		Examples: []string{
			`{"action":"list_check_runs","owner":"acme","repo":"monorepo","ref":"abc123","page":1,"per_page":50}`,
			`{"action":"list_workflow_runs","owner":"acme","repo":"monorepo","branch":"main","event":"pull_request","status":"completed","created":">=2026-03-15","page":1,"per_page":20}`,
			`{"action":"download_artifact","owner":"acme","repo":"monorepo","artifact_id":701,"extract":true}`,
		},
		OutputNotes:  "Returns normalized JSON describing GitHub CI state, including file-backed outputs for logs and artifact downloads.",
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
	response, err := executeRead(ctx, client, toolCtx, req)
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

func normalizeReadRequest(input input) (ciRequest, error) {
	coords, err := githubcommon.NormalizeRepoCoordinates(input.Owner, input.Repo)
	if err != nil {
		return ciRequest{}, err
	}
	action := strings.TrimSpace(input.Action)
	if action == "" {
		return ciRequest{}, fmt.Errorf("action must not be empty")
	}
	req := ciRequest{
		Action:        action,
		Repo:          coords,
		Ref:           strings.TrimSpace(input.Ref),
		CheckRunID:    input.CheckRunID,
		WorkflowID:    strings.TrimSpace(input.WorkflowID),
		RunID:         input.RunID,
		AttemptNumber: input.AttemptNumber,
		JobID:         input.JobID,
		Filter:        strings.ToLower(strings.TrimSpace(input.Filter)),
		Branch:        strings.TrimSpace(input.Branch),
		Event:         strings.TrimSpace(input.Event),
		Status:        strings.TrimSpace(input.Status),
		Created:       strings.TrimSpace(input.Created),
		HeadSHA:       strings.TrimSpace(input.HeadSHA),
		ArtifactID:    input.ArtifactID,
	}
	if input.Extract != nil {
		req.Extract = *input.Extract
	}
	present := map[string]bool{
		"ref":            req.Ref != "",
		"check_run_id":   req.CheckRunID != 0,
		"workflow_id":    req.WorkflowID != "",
		"run_id":         req.RunID != 0,
		"attempt_number": req.AttemptNumber != 0,
		"job_id":         req.JobID != 0,
		"filter":         req.Filter != "",
		"branch":         req.Branch != "",
		"event":          req.Event != "",
		"status":         req.Status != "",
		"created":        req.Created != "",
		"head_sha":       req.HeadSHA != "",
		"page":           input.Page != 0,
		"per_page":       input.PerPage != 0,
		"artifact_id":    req.ArtifactID != 0,
		"extract":        input.Extract != nil,
	}
	switch action {
	case ActionGetCombinedStatus:
		if err := githubcommon.RejectUnexpectedFields(action, present, "ref"); err != nil {
			return ciRequest{}, err
		}
		if req.Ref == "" {
			return ciRequest{}, fmt.Errorf("ref must not be empty")
		}
		return req, nil
	case ActionListCommitStatuses, ActionListCheckRuns:
		if err := githubcommon.RejectUnexpectedFields(action, present, "ref", "page", "per_page"); err != nil {
			return ciRequest{}, err
		}
		if req.Ref == "" {
			return ciRequest{}, fmt.Errorf("ref must not be empty")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 50)
		return req, err
	case ActionGetCheckRun:
		if err := githubcommon.RejectUnexpectedFields(action, present, "check_run_id"); err != nil {
			return ciRequest{}, err
		}
		if req.CheckRunID <= 0 {
			return ciRequest{}, fmt.Errorf("check_run_id must be a positive integer")
		}
		return req, nil
	case ActionListCheckRunAnnotations:
		if err := githubcommon.RejectUnexpectedFields(action, present, "check_run_id", "page", "per_page"); err != nil {
			return ciRequest{}, err
		}
		if req.CheckRunID <= 0 {
			return ciRequest{}, fmt.Errorf("check_run_id must be a positive integer")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 50)
		return req, err
	case ActionListWorkflows:
		if err := githubcommon.RejectUnexpectedFields(action, present, "page", "per_page"); err != nil {
			return ciRequest{}, err
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 100)
		return req, err
	case ActionGetWorkflow:
		if err := githubcommon.RejectUnexpectedFields(action, present, "workflow_id"); err != nil {
			return ciRequest{}, err
		}
		if req.WorkflowID == "" {
			return ciRequest{}, fmt.Errorf("workflow_id must not be empty")
		}
		return req, nil
	case ActionListWorkflowRuns:
		if err := githubcommon.RejectUnexpectedFields(action, present, "branch", "event", "status", "created", "head_sha", "page", "per_page"); err != nil {
			return ciRequest{}, err
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 20)
		return req, err
	case ActionListWorkflowRunsForWorkflow:
		if err := githubcommon.RejectUnexpectedFields(action, present, "workflow_id", "branch", "event", "status", "created", "head_sha", "page", "per_page"); err != nil {
			return ciRequest{}, err
		}
		if req.WorkflowID == "" {
			return ciRequest{}, fmt.Errorf("workflow_id must not be empty")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 20)
		return req, err
	case ActionGetWorkflowRun:
		if err := githubcommon.RejectUnexpectedFields(action, present, "run_id"); err != nil {
			return ciRequest{}, err
		}
		if req.RunID <= 0 {
			return ciRequest{}, fmt.Errorf("run_id must be a positive integer")
		}
		return req, nil
	case ActionGetWorkflowRunAttempt:
		if err := githubcommon.RejectUnexpectedFields(action, present, "run_id", "attempt_number"); err != nil {
			return ciRequest{}, err
		}
		if req.RunID <= 0 {
			return ciRequest{}, fmt.Errorf("run_id must be a positive integer")
		}
		if req.AttemptNumber <= 0 {
			return ciRequest{}, fmt.Errorf("attempt_number must be a positive integer")
		}
		return req, nil
	case ActionListWorkflowJobs:
		if err := githubcommon.RejectUnexpectedFields(action, present, "run_id", "filter", "page", "per_page"); err != nil {
			return ciRequest{}, err
		}
		if req.RunID <= 0 {
			return ciRequest{}, fmt.Errorf("run_id must be a positive integer")
		}
		if req.Filter == "" {
			req.Filter = "latest"
		}
		if req.Filter != "latest" && req.Filter != "all" {
			return ciRequest{}, fmt.Errorf(`filter must be "latest" or "all"`)
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 100)
		return req, err
	case ActionListWorkflowJobsForAttempt:
		if err := githubcommon.RejectUnexpectedFields(action, present, "run_id", "attempt_number", "page", "per_page"); err != nil {
			return ciRequest{}, err
		}
		if req.RunID <= 0 {
			return ciRequest{}, fmt.Errorf("run_id must be a positive integer")
		}
		if req.AttemptNumber <= 0 {
			return ciRequest{}, fmt.Errorf("attempt_number must be a positive integer")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 100)
		return req, err
	case ActionGetWorkflowJob, ActionDownloadJobLogs:
		if err := githubcommon.RejectUnexpectedFields(action, present, "job_id"); err != nil {
			return ciRequest{}, err
		}
		if req.JobID <= 0 {
			return ciRequest{}, fmt.Errorf("job_id must be a positive integer")
		}
		return req, nil
	case ActionDownloadWorkflowRunAttemptLogs:
		if err := githubcommon.RejectUnexpectedFields(action, present, "run_id", "attempt_number"); err != nil {
			return ciRequest{}, err
		}
		if req.RunID <= 0 {
			return ciRequest{}, fmt.Errorf("run_id must be a positive integer")
		}
		if req.AttemptNumber <= 0 {
			return ciRequest{}, fmt.Errorf("attempt_number must be a positive integer")
		}
		return req, nil
	case ActionDownloadWorkflowRunLogs:
		if err := githubcommon.RejectUnexpectedFields(action, present, "run_id"); err != nil {
			return ciRequest{}, err
		}
		if req.RunID <= 0 {
			return ciRequest{}, fmt.Errorf("run_id must be a positive integer")
		}
		return req, nil
	case ActionListRunArtifacts:
		if err := githubcommon.RejectUnexpectedFields(action, present, "run_id", "page", "per_page"); err != nil {
			return ciRequest{}, err
		}
		if req.RunID <= 0 {
			return ciRequest{}, fmt.Errorf("run_id must be a positive integer")
		}
		req.Pagination, err = githubcommon.NormalizePagination(input.Page, input.PerPage, 50)
		return req, err
	case ActionGetArtifact:
		if err := githubcommon.RejectUnexpectedFields(action, present, "artifact_id"); err != nil {
			return ciRequest{}, err
		}
		if req.ArtifactID <= 0 {
			return ciRequest{}, fmt.Errorf("artifact_id must be a positive integer")
		}
		return req, nil
	case ActionDownloadArtifact:
		if err := githubcommon.RejectUnexpectedFields(action, present, "artifact_id", "extract"); err != nil {
			return ciRequest{}, err
		}
		if req.ArtifactID <= 0 {
			return ciRequest{}, fmt.Errorf("artifact_id must be a positive integer")
		}
		return req, nil
	default:
		return ciRequest{}, fmt.Errorf("unsupported action %q", action)
	}
}

func executeRead(ctx context.Context, client *githubcommon.Client, toolCtx toolscore.ToolContext, req ciRequest) (githubcommon.ToolResponse, error) {
	switch req.Action {
	case ActionGetCombinedStatus:
		return executeGetCombinedStatus(ctx, client, req)
	case ActionListCommitStatuses:
		return executeListCommitStatuses(ctx, client, req)
	case ActionListCheckRuns:
		return executeListCheckRuns(ctx, client, req)
	case ActionGetCheckRun:
		return executeGetCheckRun(ctx, client, req)
	case ActionListCheckRunAnnotations:
		return executeListCheckRunAnnotations(ctx, client, req)
	case ActionListWorkflows:
		return executeListWorkflows(ctx, client, req)
	case ActionGetWorkflow:
		return executeGetWorkflow(ctx, client, req)
	case ActionListWorkflowRuns:
		return executeListWorkflowRuns(ctx, client, req)
	case ActionListWorkflowRunsForWorkflow:
		return executeListWorkflowRunsForWorkflow(ctx, client, req)
	case ActionGetWorkflowRun:
		return executeGetWorkflowRun(ctx, client, req)
	case ActionGetWorkflowRunAttempt:
		return executeGetWorkflowRunAttempt(ctx, client, req)
	case ActionListWorkflowJobs:
		return executeListWorkflowJobs(ctx, client, req)
	case ActionListWorkflowJobsForAttempt:
		return executeListWorkflowJobsForAttempt(ctx, client, req)
	case ActionGetWorkflowJob:
		return executeGetWorkflowJob(ctx, client, req)
	case ActionDownloadJobLogs:
		return executeDownloadJobLogs(ctx, client, toolCtx, req)
	case ActionDownloadWorkflowRunAttemptLogs:
		return executeDownloadWorkflowRunAttemptLogs(ctx, client, toolCtx, req)
	case ActionDownloadWorkflowRunLogs:
		return executeDownloadWorkflowRunLogs(ctx, client, toolCtx, req)
	case ActionListRunArtifacts:
		return executeListRunArtifacts(ctx, client, req)
	case ActionGetArtifact:
		return executeGetArtifact(ctx, client, req)
	case ActionDownloadArtifact:
		return executeDownloadArtifact(ctx, client, toolCtx, req)
	default:
		return githubcommon.ToolResponse{}, fmt.Errorf("unsupported action %q", req.Action)
	}
}

func executeGetCombinedStatus(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		State      string `json:"state"`
		TotalCount int    `json:"total_count"`
		Statuses   []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	_, err := client.JSON(ctx, "GET", ciPath(req.Repo, "/commits/"+url.PathEscape(req.Ref)+"/status"), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	statuses := make([]map[string]any, 0, len(response.Statuses))
	for _, status := range response.Statuses {
		statuses = append(statuses, map[string]any{
			"context": strings.TrimSpace(status.Context),
			"state":   strings.TrimSpace(status.State),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"ref":         req.Ref,
		"state":       strings.TrimSpace(response.State),
		"total_count": response.TotalCount,
		"statuses":    statuses,
	}, nil, nil, nil), nil
}

func executeListCommitStatuses(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response []struct {
		Context     string `json:"context"`
		State       string `json:"state"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}
	meta, err := client.JSON(ctx, "GET", ciPath(req.Repo, "/commits/"+url.PathEscape(req.Ref)+"/statuses"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	statuses := make([]map[string]any, 0, len(response))
	for _, status := range response {
		statuses = append(statuses, map[string]any{
			"context":     strings.TrimSpace(status.Context),
			"state":       strings.TrimSpace(status.State),
			"description": status.Description,
			"target_url":  strings.TrimSpace(status.TargetURL),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"ref":      req.Ref,
		"statuses": statuses,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListCheckRuns(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response struct {
		TotalCount int `json:"total_count"`
		CheckRuns  []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			DetailsURL string `json:"details_url"`
		} `json:"check_runs"`
	}
	meta, err := client.JSON(ctx, "GET", ciPath(req.Repo, "/commits/"+url.PathEscape(req.Ref)+"/check-runs"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	checkRuns := make([]map[string]any, 0, len(response.CheckRuns))
	for _, run := range response.CheckRuns {
		checkRuns = append(checkRuns, map[string]any{
			"id":          run.ID,
			"name":        strings.TrimSpace(run.Name),
			"status":      strings.TrimSpace(run.Status),
			"conclusion":  strings.TrimSpace(run.Conclusion),
			"details_url": strings.TrimSpace(run.DetailsURL),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"ref":         req.Ref,
		"total_count": response.TotalCount,
		"check_runs":  checkRuns,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetCheckRun(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		ID             int64  `json:"id"`
		Name           string `json:"name"`
		Status         string `json:"status"`
		Conclusion     string `json:"conclusion"`
		AnnotationsURL string `json:"annotations_url"`
		Output         struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
			Text    string `json:"text"`
		} `json:"output"`
	}
	_, err := client.JSON(ctx, "GET", ciPath(req.Repo, fmt.Sprintf("/check-runs/%d", req.CheckRunID)), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"id":         response.ID,
		"name":       strings.TrimSpace(response.Name),
		"status":     strings.TrimSpace(response.Status),
		"conclusion": strings.TrimSpace(response.Conclusion),
		"output": map[string]any{
			"title":   strings.TrimSpace(response.Output.Title),
			"summary": response.Output.Summary,
			"text":    response.Output.Text,
		},
		"annotations_url": strings.TrimSpace(response.AnnotationsURL),
	}, nil, nil, nil), nil
}

func executeListCheckRunAnnotations(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response []struct {
		Path            string `json:"path"`
		StartLine       int    `json:"start_line"`
		EndLine         int    `json:"end_line"`
		AnnotationLevel string `json:"annotation_level"`
		Message         string `json:"message"`
		Title           string `json:"title"`
	}
	meta, err := client.JSON(ctx, "GET", ciPath(req.Repo, fmt.Sprintf("/check-runs/%d/annotations", req.CheckRunID)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	annotations := make([]map[string]any, 0, len(response))
	for _, annotation := range response {
		annotations = append(annotations, map[string]any{
			"path":             strings.TrimSpace(annotation.Path),
			"start_line":       annotation.StartLine,
			"end_line":         annotation.EndLine,
			"annotation_level": strings.TrimSpace(annotation.AnnotationLevel),
			"message":          annotation.Message,
			"title":            annotation.Title,
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"check_run_id": req.CheckRunID,
		"annotations":  annotations,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListWorkflows(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response struct {
		TotalCount int `json:"total_count"`
		Workflows  []struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Path  string `json:"path"`
			State string `json:"state"`
		} `json:"workflows"`
	}
	meta, err := client.JSON(ctx, "GET", ciPath(req.Repo, "/actions/workflows"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	workflows := make([]map[string]any, 0, len(response.Workflows))
	for _, workflow := range response.Workflows {
		workflows = append(workflows, map[string]any{
			"id":    workflow.ID,
			"name":  strings.TrimSpace(workflow.Name),
			"path":  strings.TrimSpace(workflow.Path),
			"state": strings.TrimSpace(workflow.State),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"total_count": response.TotalCount,
		"workflows":   workflows,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetWorkflow(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Path      string `json:"path"`
		State     string `json:"state"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	_, err := client.JSON(ctx, "GET", ciPath(req.Repo, "/actions/workflows/"+url.PathEscape(req.WorkflowID)), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"id":         response.ID,
		"name":       strings.TrimSpace(response.Name),
		"path":       strings.TrimSpace(response.Path),
		"state":      strings.TrimSpace(response.State),
		"created_at": strings.TrimSpace(response.CreatedAt),
		"updated_at": strings.TrimSpace(response.UpdatedAt),
	}, nil, nil, nil), nil
}

func executeListWorkflowRuns(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	query := workflowRunQuery(req)
	var response struct {
		TotalCount   int `json:"total_count"`
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			WorkflowID int64  `json:"workflow_id"`
			HeadBranch string `json:"head_branch"`
			HeadSHA    string `json:"head_sha"`
			RunAttempt int64  `json:"run_attempt"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	meta, err := client.JSON(ctx, "GET", ciPath(req.Repo, "/actions/runs"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	runs := make([]map[string]any, 0, len(response.WorkflowRuns))
	for _, run := range response.WorkflowRuns {
		runs = append(runs, map[string]any{
			"id":          run.ID,
			"name":        strings.TrimSpace(run.Name),
			"workflow_id": run.WorkflowID,
			"head_branch": strings.TrimSpace(run.HeadBranch),
			"head_sha":    strings.TrimSpace(run.HeadSHA),
			"run_attempt": run.RunAttempt,
			"status":      strings.TrimSpace(run.Status),
			"conclusion":  strings.TrimSpace(run.Conclusion),
		})
	}
	var warnings []string
	if req.Branch != "" || req.Event != "" || req.Status != "" || req.Created != "" || req.HeadSHA != "" {
		warnings = append(warnings, "GitHub returns at most 1000 workflow runs per filtered search")
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"total_count":   response.TotalCount,
		"workflow_runs": runs,
	}, warnings, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListWorkflowRunsForWorkflow(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	if req.Branch != "" {
		query.Set("branch", req.Branch)
	}
	if req.Event != "" {
		query.Set("event", req.Event)
	}
	if req.Status != "" {
		query.Set("status", req.Status)
	}
	if req.Created != "" {
		query.Set("created", req.Created)
	}
	if req.HeadSHA != "" {
		query.Set("head_sha", req.HeadSHA)
	}
	var response struct {
		TotalCount   int `json:"total_count"`
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			RunAttempt int64  `json:"run_attempt"`
			HeadSHA    string `json:"head_sha"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	meta, err := client.JSON(ctx, "GET", ciPath(req.Repo, "/actions/workflows/"+url.PathEscape(req.WorkflowID)+"/runs"), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	runs := make([]map[string]any, 0, len(response.WorkflowRuns))
	for _, run := range response.WorkflowRuns {
		runs = append(runs, map[string]any{
			"id":          run.ID,
			"run_attempt": run.RunAttempt,
			"head_sha":    strings.TrimSpace(run.HeadSHA),
			"conclusion":  strings.TrimSpace(run.Conclusion),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"workflow_id":   req.WorkflowID,
		"total_count":   response.TotalCount,
		"workflow_runs": runs,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetWorkflowRun(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		ID         int64  `json:"id"`
		WorkflowID int64  `json:"workflow_id"`
		Name       string `json:"name"`
		HeadBranch string `json:"head_branch"`
		HeadSHA    string `json:"head_sha"`
		Event      string `json:"event"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		RunAttempt int64  `json:"run_attempt"`
		LogsURL    string `json:"logs_url"`
	}
	_, err := client.JSON(ctx, "GET", ciPath(req.Repo, fmt.Sprintf("/actions/runs/%d", req.RunID)), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"id":          response.ID,
		"workflow_id": response.WorkflowID,
		"name":        strings.TrimSpace(response.Name),
		"head_branch": strings.TrimSpace(response.HeadBranch),
		"head_sha":    strings.TrimSpace(response.HeadSHA),
		"event":       strings.TrimSpace(response.Event),
		"status":      strings.TrimSpace(response.Status),
		"conclusion":  strings.TrimSpace(response.Conclusion),
		"run_attempt": response.RunAttempt,
		"logs_url":    strings.TrimSpace(response.LogsURL),
	}, nil, nil, nil), nil
}

func executeGetWorkflowRunAttempt(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	var response struct {
		RunID      int64  `json:"id"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	_, err := client.JSON(ctx, "GET", ciPath(req.Repo, fmt.Sprintf("/actions/runs/%d/attempts/%d", req.RunID, req.AttemptNumber)), nil, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	logsURL, _ := client.URL(ciPath(req.Repo, fmt.Sprintf("/actions/runs/%d/attempts/%d/logs", req.RunID, req.AttemptNumber)), nil)
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"run_id":         req.RunID,
		"attempt_number": req.AttemptNumber,
		"head_sha":       strings.TrimSpace(response.HeadSHA),
		"status":         strings.TrimSpace(response.Status),
		"conclusion":     strings.TrimSpace(response.Conclusion),
		"logs_url":       logsURL,
	}, nil, nil, nil), nil
}

func executeListWorkflowJobs(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	if req.Filter != "" {
		query.Set("filter", req.Filter)
	}
	var response struct {
		TotalCount int `json:"total_count"`
		Jobs       []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			RunAttempt int64  `json:"run_attempt"`
		} `json:"jobs"`
	}
	meta, err := client.JSON(ctx, "GET", ciPath(req.Repo, fmt.Sprintf("/actions/runs/%d/jobs", req.RunID)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	jobs := make([]map[string]any, 0, len(response.Jobs))
	for _, job := range response.Jobs {
		jobs = append(jobs, map[string]any{
			"id":          job.ID,
			"name":        strings.TrimSpace(job.Name),
			"status":      strings.TrimSpace(job.Status),
			"conclusion":  strings.TrimSpace(job.Conclusion),
			"run_attempt": job.RunAttempt,
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"run_id":      req.RunID,
		"total_count": response.TotalCount,
		"jobs":        jobs,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeListWorkflowJobsForAttempt(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response struct {
		TotalCount int `json:"total_count"`
		Jobs       []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"jobs"`
	}
	meta, err := client.JSON(ctx, "GET", ciPath(req.Repo, fmt.Sprintf("/actions/runs/%d/attempts/%d/jobs", req.RunID, req.AttemptNumber)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	jobs := make([]map[string]any, 0, len(response.Jobs))
	for _, job := range response.Jobs {
		jobs = append(jobs, map[string]any{
			"id":         job.ID,
			"name":       strings.TrimSpace(job.Name),
			"status":     strings.TrimSpace(job.Status),
			"conclusion": strings.TrimSpace(job.Conclusion),
		})
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"run_id":         req.RunID,
		"attempt_number": req.AttemptNumber,
		"total_count":    response.TotalCount,
		"jobs":           jobs,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetWorkflowJob(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	job, err := getWorkflowJob(ctx, client, req)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"id":          job.ID,
		"name":        strings.TrimSpace(job.Name),
		"run_id":      job.RunID,
		"run_attempt": job.RunAttempt,
		"status":      strings.TrimSpace(job.Status),
		"conclusion":  strings.TrimSpace(job.Conclusion),
		"steps":       job.Steps,
	}, nil, nil, nil), nil
}

func executeDownloadJobLogs(ctx context.Context, client *githubcommon.Client, toolCtx toolscore.ToolContext, req ciRequest) (githubcommon.ToolResponse, error) {
	job, err := getWorkflowJob(ctx, client, req)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	relativePath := filepath.ToSlash(filepath.Join("github", "actions", fmt.Sprintf("run-%d", job.RunID), fmt.Sprintf("job-%d.log", req.JobID)))
	download, err := client.Download(
		ctx,
		ciPath(req.Repo, fmt.Sprintf("/actions/jobs/%d/logs", req.JobID)),
		nil,
		"application/octet-stream",
		relativePath,
		"GitHub Actions job log",
		toolCtx.FileSystem(),
		toolCtx.ResolvePath,
		false,
		"",
	)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"job_id":     req.JobID,
		"redirected": download.Meta.Redirected,
		"format":     "text",
	}, nil, nil, download.Files), nil
}

func executeDownloadWorkflowRunAttemptLogs(ctx context.Context, client *githubcommon.Client, toolCtx toolscore.ToolContext, req ciRequest) (githubcommon.ToolResponse, error) {
	relativePath := filepath.ToSlash(filepath.Join("github", "actions", fmt.Sprintf("run-%d-attempt-%d-logs.zip", req.RunID, req.AttemptNumber)))
	download, err := client.Download(
		ctx,
		ciPath(req.Repo, fmt.Sprintf("/actions/runs/%d/attempts/%d/logs", req.RunID, req.AttemptNumber)),
		nil,
		"application/octet-stream",
		relativePath,
		"Workflow run attempt log archive",
		toolCtx.FileSystem(),
		toolCtx.ResolvePath,
		false,
		"",
	)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"run_id":         req.RunID,
		"attempt_number": req.AttemptNumber,
		"redirected":     download.Meta.Redirected,
		"format":         "zip",
	}, []string{"Download URLs expire quickly and should not be cached"}, nil, download.Files), nil
}

func executeDownloadWorkflowRunLogs(ctx context.Context, client *githubcommon.Client, toolCtx toolscore.ToolContext, req ciRequest) (githubcommon.ToolResponse, error) {
	relativePath := filepath.ToSlash(filepath.Join("github", "actions", fmt.Sprintf("run-%d-logs.zip", req.RunID)))
	download, err := client.Download(
		ctx,
		ciPath(req.Repo, fmt.Sprintf("/actions/runs/%d/logs", req.RunID)),
		nil,
		"application/octet-stream",
		relativePath,
		"Workflow run log archive",
		toolCtx.FileSystem(),
		toolCtx.ResolvePath,
		false,
		"",
	)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"run_id":     req.RunID,
		"redirected": download.Meta.Redirected,
		"format":     "zip",
	}, []string{"Use attempt-specific logs when comparing reruns for flaky detection"}, nil, download.Files), nil
}

func executeListRunArtifacts(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	query := paginationQuery(req.Pagination)
	var response struct {
		TotalCount int `json:"total_count"`
		Artifacts  []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			SizeInBytes int64  `json:"size_in_bytes"`
			Expired     bool   `json:"expired"`
			WorkflowRun *struct {
				HeadSHA string `json:"head_sha"`
			} `json:"workflow_run"`
		} `json:"artifacts"`
	}
	meta, err := client.JSON(ctx, "GET", ciPath(req.Repo, fmt.Sprintf("/actions/runs/%d/artifacts", req.RunID)), query, "", &response)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	artifacts := make([]map[string]any, 0, len(response.Artifacts))
	for _, artifact := range response.Artifacts {
		item := map[string]any{
			"id":            artifact.ID,
			"name":          strings.TrimSpace(artifact.Name),
			"size_in_bytes": artifact.SizeInBytes,
			"expired":       artifact.Expired,
		}
		if artifact.WorkflowRun != nil {
			item["head_sha"] = strings.TrimSpace(artifact.WorkflowRun.HeadSHA)
		}
		artifacts = append(artifacts, item)
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"run_id":      req.RunID,
		"total_count": response.TotalCount,
		"artifacts":   artifacts,
	}, nil, githubcommon.PageInfoFromLinkHeader(req.Pagination, meta.Header.Get("Link")), nil), nil
}

func executeGetArtifact(ctx context.Context, client *githubcommon.Client, req ciRequest) (githubcommon.ToolResponse, error) {
	artifact, err := getArtifact(ctx, client, req)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	data := map[string]any{
		"id":            artifact.ID,
		"name":          strings.TrimSpace(artifact.Name),
		"size_in_bytes": artifact.SizeInBytes,
		"expired":       artifact.Expired,
		"expires_at":    strings.TrimSpace(artifact.ExpiresAt),
	}
	if artifact.WorkflowRun != nil {
		data["workflow_run"] = map[string]any{
			"id":          artifact.WorkflowRun.ID,
			"head_branch": strings.TrimSpace(artifact.WorkflowRun.HeadBranch),
			"head_sha":    strings.TrimSpace(artifact.WorkflowRun.HeadSHA),
		}
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, data, nil, nil, nil), nil
}

func executeDownloadArtifact(ctx context.Context, client *githubcommon.Client, toolCtx toolscore.ToolContext, req ciRequest) (githubcommon.ToolResponse, error) {
	artifact, err := getArtifact(ctx, client, req)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	archivePath := filepath.ToSlash(filepath.Join("github", "actions", "artifacts", fmt.Sprintf("%d.zip", req.ArtifactID)))
	extractRoot := ""
	if req.Extract {
		extractRoot = filepath.ToSlash(filepath.Join("github", "actions", "artifacts", fmt.Sprintf("%d", req.ArtifactID)))
	}
	download, err := client.Download(
		ctx,
		ciPath(req.Repo, fmt.Sprintf("/actions/artifacts/%d/zip", req.ArtifactID)),
		nil,
		"application/octet-stream",
		archivePath,
		"Raw artifact archive",
		toolCtx.FileSystem(),
		toolCtx.ResolvePath,
		req.Extract,
		extractRoot,
	)
	if err != nil {
		return githubcommon.ToolResponse{}, err
	}
	return githubcommon.SuccessResponse(ToolName, req.Action, map[string]any{
		"artifact_id":    req.ArtifactID,
		"name":           strings.TrimSpace(artifact.Name),
		"redirected":     download.Meta.Redirected,
		"archive_format": "zip",
		"extract":        req.Extract,
	}, nil, nil, download.Files), nil
}

type workflowJob struct {
	ID         int64
	Name       string
	RunID      int64
	RunAttempt int64
	Status     string
	Conclusion string
	Steps      []map[string]any
}

type artifactRecord struct {
	ID          int64
	Name        string
	SizeInBytes int64
	Expired     bool
	ExpiresAt   string
	WorkflowRun *struct {
		ID         int64
		HeadBranch string
		HeadSHA    string
	}
}

func getWorkflowJob(ctx context.Context, client *githubcommon.Client, req ciRequest) (workflowJob, error) {
	var response struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		RunID      int64  `json:"run_id"`
		RunAttempt int64  `json:"run_attempt"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Steps      []struct {
			Number     int    `json:"number"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"steps"`
	}
	_, err := client.JSON(ctx, "GET", ciPath(req.Repo, fmt.Sprintf("/actions/jobs/%d", req.JobID)), nil, "", &response)
	if err != nil {
		return workflowJob{}, err
	}
	steps := make([]map[string]any, 0, len(response.Steps))
	for _, step := range response.Steps {
		steps = append(steps, map[string]any{
			"number":     step.Number,
			"name":       strings.TrimSpace(step.Name),
			"status":     strings.TrimSpace(step.Status),
			"conclusion": strings.TrimSpace(step.Conclusion),
		})
	}
	return workflowJob{
		ID:         response.ID,
		Name:       response.Name,
		RunID:      response.RunID,
		RunAttempt: response.RunAttempt,
		Status:     response.Status,
		Conclusion: response.Conclusion,
		Steps:      steps,
	}, nil
}

func getArtifact(ctx context.Context, client *githubcommon.Client, req ciRequest) (artifactRecord, error) {
	var response struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		SizeInBytes int64  `json:"size_in_bytes"`
		Expired     bool   `json:"expired"`
		ExpiresAt   string `json:"expires_at"`
		WorkflowRun *struct {
			ID         int64  `json:"id"`
			HeadBranch string `json:"head_branch"`
			HeadSHA    string `json:"head_sha"`
		} `json:"workflow_run"`
	}
	_, err := client.JSON(ctx, "GET", ciPath(req.Repo, fmt.Sprintf("/actions/artifacts/%d", req.ArtifactID)), nil, "", &response)
	if err != nil {
		return artifactRecord{}, err
	}
	record := artifactRecord{
		ID:          response.ID,
		Name:        response.Name,
		SizeInBytes: response.SizeInBytes,
		Expired:     response.Expired,
		ExpiresAt:   response.ExpiresAt,
	}
	if response.WorkflowRun != nil {
		record.WorkflowRun = &struct {
			ID         int64
			HeadBranch string
			HeadSHA    string
		}{
			ID:         response.WorkflowRun.ID,
			HeadBranch: response.WorkflowRun.HeadBranch,
			HeadSHA:    response.WorkflowRun.HeadSHA,
		}
	}
	return record, nil
}

func ciPath(repo githubcommon.RepoCoordinates, suffix string) string {
	return fmt.Sprintf("/repos/%s/%s%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Repo), suffix)
}

func paginationQuery(p githubcommon.Pagination) url.Values {
	query := url.Values{}
	query.Set("page", fmt.Sprintf("%d", p.Page))
	query.Set("per_page", fmt.Sprintf("%d", p.PerPage))
	return query
}

func workflowRunQuery(req ciRequest) url.Values {
	query := paginationQuery(req.Pagination)
	if req.Branch != "" {
		query.Set("branch", req.Branch)
	}
	if req.Event != "" {
		query.Set("event", req.Event)
	}
	if req.Status != "" {
		query.Set("status", req.Status)
	}
	if req.Created != "" {
		query.Set("created", req.Created)
	}
	if req.HeadSHA != "" {
		query.Set("head_sha", req.HeadSHA)
	}
	return query
}
