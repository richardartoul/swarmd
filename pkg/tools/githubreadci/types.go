package githubreadci

import "github.com/richardartoul/swarmd/pkg/tools/githubcommon"

const (
	ToolName = "github_read_ci"

	ActionGetCombinedStatus             = "get_combined_status"
	ActionListCommitStatuses           = "list_commit_statuses"
	ActionListCheckRuns                = "list_check_runs"
	ActionGetCheckRun                  = "get_check_run"
	ActionListCheckRunAnnotations      = "list_check_run_annotations"
	ActionListWorkflows                = "list_workflows"
	ActionGetWorkflow                  = "get_workflow"
	ActionListWorkflowRuns             = "list_workflow_runs"
	ActionListWorkflowRunsForWorkflow  = "list_workflow_runs_for_workflow"
	ActionGetWorkflowRun               = "get_workflow_run"
	ActionGetWorkflowRunAttempt        = "get_workflow_run_attempt"
	ActionListWorkflowJobs             = "list_workflow_jobs"
	ActionListWorkflowJobsForAttempt   = "list_workflow_jobs_for_attempt"
	ActionGetWorkflowJob               = "get_workflow_job"
	ActionDownloadJobLogs             = "download_job_logs"
	ActionDownloadWorkflowRunAttemptLogs = "download_workflow_run_attempt_logs"
	ActionDownloadWorkflowRunLogs      = "download_workflow_run_logs"
	ActionListRunArtifacts             = "list_run_artifacts"
	ActionGetArtifact                  = "get_artifact"
	ActionDownloadArtifact             = "download_artifact"
)

type input struct {
	Action        string `json:"action"`
	Owner         string `json:"owner"`
	Repo          string `json:"repo"`
	Ref           string `json:"ref,omitempty"`
	CheckRunID    int64  `json:"check_run_id,omitempty"`
	WorkflowID    string `json:"workflow_id,omitempty"`
	RunID         int64  `json:"run_id,omitempty"`
	AttemptNumber int64  `json:"attempt_number,omitempty"`
	JobID         int64  `json:"job_id,omitempty"`
	Filter        string `json:"filter,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Event         string `json:"event,omitempty"`
	Status        string `json:"status,omitempty"`
	Created       string `json:"created,omitempty"`
	HeadSHA       string `json:"head_sha,omitempty"`
	Page          int    `json:"page,omitempty"`
	PerPage       int    `json:"per_page,omitempty"`
	ArtifactID    int64  `json:"artifact_id,omitempty"`
	Extract       *bool  `json:"extract,omitempty"`
}

type Input = input

type ciRequest struct {
	Action        string
	Repo          githubcommon.RepoCoordinates
	Ref           string
	CheckRunID    int64
	WorkflowID    string
	RunID         int64
	AttemptNumber int64
	JobID         int64
	Filter        string
	Branch        string
	Event         string
	Status        string
	Created       string
	HeadSHA       string
	Pagination    githubcommon.Pagination
	ArtifactID    int64
	Extract       bool
}
