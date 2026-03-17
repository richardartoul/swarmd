package server

import (
	"fmt"
	"os"
	"sort"
	"strings"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"
)

func ValidateReferencedToolEnv(specs []AgentSpec, lookupEnv func(string) string) error {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	missingByTool := make(map[string][]string)
	for _, spec := range specs {
		tools, err := normalizeAgentTools(spec.Tools)
		if err != nil {
			return err
		}
		for _, tool := range tools {
			for _, envVar := range toolregistry.RequiredEnvForTool(tool.ID) {
				if strings.TrimSpace(lookupEnv(envVar)) != "" {
					continue
				}
				missingByTool[tool.ID] = toolscommon.AppendUniqueString(missingByTool[tool.ID], envVar)
			}
		}
	}
	if len(missingByTool) == 0 {
		return nil
	}
	toolIDs := make([]string, 0, len(missingByTool))
	for toolID := range missingByTool {
		toolIDs = append(toolIDs, toolID)
	}
	sort.Strings(toolIDs)
	parts := make([]string, 0, len(toolIDs))
	for _, toolID := range toolIDs {
		envVars := append([]string(nil), missingByTool[toolID]...)
		sort.Strings(envVars)
		parts = append(parts, fmt.Sprintf("%s=[%s]", toolID, strings.Join(envVars, ", ")))
	}
	return fmt.Errorf("missing required environment variables for referenced tools: %s", strings.Join(parts, "; "))
}
