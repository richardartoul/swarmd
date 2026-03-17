// See LICENSE for licensing information

package agent

import toolregistry "github.com/richardartoul/swarmd/pkg/tools/registry"

const (
	ToolNameListDir     = "list_dir"
	ToolNameReadFile    = "read_file"
	ToolNameGrepFiles   = "grep_files"
	ToolNameWebSearch   = "web_search"
	ToolNameReadWebPage = "read_web_page"
	ToolNameHTTPRequest = "http_request"
	ToolNameApplyPatch  = "apply_patch"
	ToolNameRunShell    = "run_shell"
)

var builtInToolDefinitions = loadBuiltInToolDefinitions()

func loadBuiltInToolDefinitions() map[string]ToolDefinition {
	definitions, err := toolregistry.ResolveToolDefinitions(nil, true)
	if err != nil {
		panic(err)
	}
	builtIns := make(map[string]ToolDefinition, len(definitions))
	for _, definition := range definitions {
		builtIns[definition.Name] = definition
	}
	return builtIns
}
