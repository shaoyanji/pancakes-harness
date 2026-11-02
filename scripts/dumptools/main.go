// Dumps the harness's own tool registry as OpenAI-form tool JSON.
// Run from the module root: go run ./tmp_dumptools > scripts/needle_tools.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"pancakes-harness/internal/tooling"
)

type function struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type tool struct {
	Type     string   `json:"type"`
	Function function `json:"function"`
}

// paramSchema turns the registry's input map ({"pattern": "string", ...}) into a
// JSON-schema object. Every declared input is required: the handlers reject a
// missing value, so the schema should not advertise it as optional.
func paramSchema(input map[string]any) map[string]any {
	names := make([]string, 0, len(input))
	for name := range input {
		names = append(names, name)
	}
	sort.Strings(names)

	properties := make(map[string]any, len(names))
	for _, name := range names {
		properties[name] = map[string]any{"type": fmt.Sprint(input[name])}
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if len(names) > 0 {
		schema["required"] = names
	}
	return schema
}

func main() {
	registry := tooling.NewRegistry()
	tooling.RegisterBuiltinTools(registry)

	tools := make([]tool, 0, 8)
	for _, t := range registry.List() {
		tools = append(tools, tool{
			Type: "function",
			Function: function{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  paramSchema(t.Schema.Input),
			},
		})
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tools); err != nil {
		panic(err)
	}
}
