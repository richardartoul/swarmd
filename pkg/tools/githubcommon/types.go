package githubcommon

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolscore "github.com/richardartoul/swarmd/pkg/tools/core"
)

const (
	GitHubTokenEnvVar        = "GITHUB_TOKEN"
	GitHubAPIBaseURLEnvVar   = "GITHUB_API_BASE_URL"
	DefaultGitHubAPIBaseURL  = "https://api.github.com"
	GitHubAPIVersion         = "2022-11-28"
	DefaultPage              = 1
	DefaultPerPage           = 30
	MaxPerPage               = 100
	DefaultJSONResponseBytes = 8 << 20
	MaxDownloadBytes         = 64 << 20
	MaxExtractedFiles        = 512
	MaxExtractedBytes        = 64 << 20
)

type RepoCoordinates struct {
	Owner string
	Repo  string
}

type Pagination struct {
	Page    int
	PerPage int
}

type PageInfo struct {
	Page        int  `json:"page"`
	PerPage     int  `json:"per_page"`
	HasNextPage bool `json:"has_next_page"`
	NextPage    *int `json:"next_page"`
}

type FileReference = toolscore.FileReference

type ToolError struct {
	Code             string `json:"code"`
	Message          string `json:"message"`
	Status           int    `json:"status,omitempty"`
	DocumentationURL string `json:"documentation_url,omitempty"`
}

type ToolResponse struct {
	Tool     string          `json:"tool"`
	Action   string          `json:"action"`
	OK       bool            `json:"ok"`
	Data     any             `json:"data,omitempty"`
	Error    *ToolError      `json:"error,omitempty"`
	Warnings []string        `json:"warnings"`
	PageInfo *PageInfo       `json:"page_info"`
	Files    []FileReference `json:"files"`
}

type APIError struct {
	Status           int
	Code             string
	Message          string
	DocumentationURL string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	switch {
	case strings.TrimSpace(e.Code) != "" && strings.TrimSpace(e.Message) != "":
		return fmt.Sprintf("%s: %s", strings.TrimSpace(e.Code), strings.TrimSpace(e.Message))
	case strings.TrimSpace(e.Message) != "":
		return strings.TrimSpace(e.Message)
	case strings.TrimSpace(e.Code) != "":
		return strings.TrimSpace(e.Code)
	default:
		return "GitHub API error"
	}
}

func (e *APIError) ToolError() *ToolError {
	if e == nil {
		return nil
	}
	return &ToolError{
		Code:             strings.TrimSpace(e.Code),
		Message:          strings.TrimSpace(e.Message),
		Status:           e.Status,
		DocumentationURL: strings.TrimSpace(e.DocumentationURL),
	}
}

func SuccessResponse(toolName, action string, data any, warnings []string, pageInfo *PageInfo, files []FileReference) ToolResponse {
	return ToolResponse{
		Tool:     strings.TrimSpace(toolName),
		Action:   strings.TrimSpace(action),
		OK:       true,
		Data:     data,
		Warnings: normalizeWarnings(warnings),
		PageInfo: pageInfo,
		Files:    normalizeFiles(files),
	}
}

func FailureResponse(toolName, action string, err *APIError, warnings []string, pageInfo *PageInfo, files []FileReference) ToolResponse {
	if err == nil {
		err = &APIError{
			Code:    "github_api_error",
			Message: "GitHub request failed",
		}
	}
	return ToolResponse{
		Tool:     strings.TrimSpace(toolName),
		Action:   strings.TrimSpace(action),
		OK:       false,
		Error:    err.ToolError(),
		Warnings: normalizeWarnings(warnings),
		PageInfo: pageInfo,
		Files:    normalizeFiles(files),
	}
}

func NormalizeRepoCoordinates(owner, repo string) (RepoCoordinates, error) {
	coords := RepoCoordinates{
		Owner: strings.TrimSpace(owner),
		Repo:  strings.TrimSpace(repo),
	}
	if coords.Owner == "" {
		return RepoCoordinates{}, fmt.Errorf("owner must not be empty")
	}
	if coords.Repo == "" {
		return RepoCoordinates{}, fmt.Errorf("repo must not be empty")
	}
	return coords, nil
}

func NormalizePagination(page, perPage, defaultPerPage int) (Pagination, error) {
	if page < 0 {
		return Pagination{}, fmt.Errorf("page must be >= 1")
	}
	if perPage < 0 {
		return Pagination{}, fmt.Errorf("per_page must not be negative")
	}
	if defaultPerPage <= 0 {
		defaultPerPage = DefaultPerPage
	}
	if page == 0 {
		page = DefaultPage
	}
	if perPage == 0 {
		perPage = defaultPerPage
	}
	if perPage > MaxPerPage {
		perPage = MaxPerPage
	}
	return Pagination{
		Page:    page,
		PerPage: perPage,
	}, nil
}

func PageInfoForPagination(p Pagination, nextPage *int) *PageInfo {
	if p.Page <= 0 || p.PerPage <= 0 {
		return nil
	}
	return &PageInfo{
		Page:        p.Page,
		PerPage:     p.PerPage,
		HasNextPage: nextPage != nil,
		NextPage:    nextPage,
	}
}

func PageInfoFromLinkHeader(p Pagination, linkHeader string) *PageInfo {
	if p.Page <= 0 || p.PerPage <= 0 {
		return nil
	}
	nextPage, hasNextPage := ParseNextLink(linkHeader)
	return &PageInfo{
		Page:        p.Page,
		PerPage:     p.PerPage,
		HasNextPage: hasNextPage,
		NextPage:    nextPage,
	}
}

func ParseNextLink(linkHeader string) (*int, bool) {
	linkHeader = strings.TrimSpace(linkHeader)
	if linkHeader == "" {
		return nil, false
	}
	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		if part == "" || (!strings.Contains(part, `rel="next"`) && !strings.Contains(part, "rel=next")) {
			continue
		}
		start := strings.Index(part, "<")
		end := strings.Index(part, ">")
		if start < 0 || end <= start+1 {
			return nil, true
		}
		target := part[start+1 : end]
		parsed, err := url.Parse(target)
		if err != nil {
			return nil, true
		}
		pageText := strings.TrimSpace(parsed.Query().Get("page"))
		if pageText == "" {
			return nil, true
		}
		page, err := strconv.Atoi(pageText)
		if err != nil || page <= 0 {
			return nil, true
		}
		return &page, true
	}
	return nil, false
}

func ParseNextPage(linkHeader string) *int {
	nextPage, _ := ParseNextLink(linkHeader)
	return nextPage
}

func NormalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func NormalizeRepoPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "\\", "/")
	if raw == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	segments := strings.Split(strings.Trim(raw, "/"), "/")
	normalized := make([]string, 0, len(segments))
	for _, segment := range segments {
		switch segment {
		case "":
			continue
		case ".", "..":
			return "", fmt.Errorf("path contains an invalid segment %q", segment)
		default:
			normalized = append(normalized, segment)
		}
	}
	if len(normalized) == 0 {
		return "", fmt.Errorf("path must not be empty")
	}
	return strings.Join(normalized, "/"), nil
}

func RejectUnexpectedFields(action string, present map[string]bool, allowed ...string) error {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		allowedSet[name] = struct{}{}
	}
	var unexpected []string
	for field, isPresent := range present {
		if !isPresent {
			continue
		}
		if _, ok := allowedSet[field]; ok {
			continue
		}
		unexpected = append(unexpected, field)
	}
	if len(unexpected) == 0 {
		return nil
	}
	sort.Strings(unexpected)
	return fmt.Errorf("%s does not accept %s", strings.TrimSpace(action), strings.Join(unexpected, ", "))
}

func StringArraySchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": map[string]any{
			"type": "string",
		},
	}
}

func ArraySchema(description string, itemSchema map[string]any) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       itemSchema,
	}
}

func StringMapSchema(description string) map[string]any {
	return toolscommon.StringMapSchema(description)
}

func normalizeWarnings(warnings []string) []string {
	normalized := NormalizeStringSlice(warnings)
	if len(normalized) == 0 {
		return []string{}
	}
	return normalized
}

func normalizeFiles(files []FileReference) []FileReference {
	return toolscommon.NormalizeFileReferences(files)
}
