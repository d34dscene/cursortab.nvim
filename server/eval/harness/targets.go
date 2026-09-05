package harness

import (
	"os"

	mercuryclient "cursortab/client/mercuryapi"
)

// DefaultTargets returns the built-in target definitions. Targets that need
// a URL read it from the CURSORTAB_EVAL_URL environment variable.
func DefaultTargets() map[string]Target {
	url := os.Getenv("CURSORTAB_EVAL_URL")

	return map[string]Target{
		"mercuryapi": {Name: "mercuryapi", Type: "mercuryapi", Model: mercuryclient.Model},
		"copilot":    {Name: "copilot", Type: "copilot"},
		"windsurf":   {Name: "windsurf", Type: "windsurf"},

		"sweep-next-edit-0.5B": {Name: "sweep-next-edit-0.5B", Type: "sweep", Model: "sweep-next-edit-0.5B", URL: url},
		"sweep-next-edit-1.5B": {Name: "sweep-next-edit-1.5B", Type: "sweep", Model: "sweep-next-edit-1.5B", URL: url},
		"sweep-next-edit-7B":   {Name: "sweep-next-edit-7B", Type: "sweep", Model: "sweep-next-edit-7B", URL: url},

		"zeta":   {Name: "zeta", Type: "zeta", Model: "zeta", URL: url},
		"zeta-2": {Name: "zeta-2", Type: "zeta-2", Model: "zeta-2", URL: url},

		"qwen3.5-0.8B":    {Name: "qwen3.5-0.8B", Type: "fim", Model: "Qwen/Qwen3.5-0.8B", URL: url},
		"qwen3.5-2B":      {Name: "qwen3.5-2B", Type: "fim", Model: "Qwen/Qwen3.5-2B", URL: url},
		"qwen3.5-4B":      {Name: "qwen3.5-4B", Type: "fim", Model: "Qwen/Qwen3.5-4B", URL: url},
		"qwen3.6-27B":     {Name: "qwen3.6-27B", Type: "fim", Model: "Qwen/Qwen3.6-27B", URL: url},
		"qwen3.6-35B-A3B": {Name: "qwen3.6-35B-A3B", Type: "fim", Model: "Qwen/Qwen3.6-35B-A3B", URL: url},

		// Local llama.cpp router targets (set CURSORTAB_EVAL_URL to the server).
		"mellum-4b":          {Name: "mellum-4b", Type: "fim", Model: "mellum-4b-dpo-all.Q8_0", URL: url},
		"qwen3.5-0.8B-local": {Name: "qwen3.5-0.8B-local", Type: "fim", Model: "Qwen3.5-0.8B-Q8_0", URL: url},
		"qwen2.5-coder-7B":   {Name: "qwen2.5-coder-7B", Type: "fim", Model: "Qwen2.5-Coder-7B.Q8_0", URL: url},
		"sweep-v2-7B":        {Name: "sweep-v2-7B", Type: "sweep", Model: "sweep-next-edit-v2-7B-Q5_K_M", URL: url},
		"zeta-2-local":       {Name: "zeta-2-local", Type: "zeta-2", Model: "zed-industries_zeta-2-Q5_K_M", URL: url},
		"zeta-2.1":           {Name: "zeta-2.1", Type: "zeta-2.1", Model: "zeta-2.1.Q8_0", URL: url},
		"zeta-2.1-q4":        {Name: "zeta-2.1-q4", Type: "zeta-2.1", Model: "zeta-2.1.Q4_K_M", URL: url},
	}
}
