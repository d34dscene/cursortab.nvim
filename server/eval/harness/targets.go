package harness

import (
	"os"

	"cursortab/provider"
)

// DefaultTargets returns the built-in target definitions for the user's
// llama.cpp router. Every target pins an explicit dialect so harness prompts
// are deterministic and model auto-detection never runs. The base URL comes
// from CURSORTAB_EVAL_URL and defaults to the local router.
func DefaultTargets() map[string]Target {
	url := os.Getenv("CURSORTAB_EVAL_URL")
	if url == "" {
		url = "http://localhost:8000"
	}

	return map[string]Target{
		"mellum-4b":             {Name: "mellum-4b", Dialect: "fim-mellum", Role: provider.RoleType, Model: "mellum-4b-dpo-all.Q8_0", URL: url},
		"sweep-next-edit-v2-7B": {Name: "sweep-next-edit-v2-7B", Dialect: "edit-sweep", Role: provider.RoleEdit, Model: "sweep-next-edit-v2-7B-Q5_K_M", URL: url},
		"zeta-2.1":              {Name: "zeta-2.1", Dialect: "edit-zeta21", Role: provider.RoleEdit, Model: "zeta-2.1.Q8_0", URL: url},
		"zeta-2":                {Name: "zeta-2", Dialect: "edit-zeta2", Role: provider.RoleEdit, Model: "zed-industries_zeta-2-Q5_K_M", URL: url},
		"qwen3.5-0.8B":          {Name: "qwen3.5-0.8B", Dialect: "fim-qwen", Role: provider.RoleType, Model: "Qwen3.5-0.8B-Q8_0", URL: url},
	}
}
