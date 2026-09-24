package provider

import (
	"maps"
	"slices"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/types"
)

// dialect is one entry of the family table: how a model id matches, which
// role it fills, its FIM token preset, and its protocol functions.
type dialect struct {
	name      string
	role      Role
	match     func(model string) bool
	tokens    func() *types.FIMTokenConfig
	materials func(cfg *types.ProviderConfig) sourcectx.Materials
	build     func(p *providerBase, state *RequestState) (*openai.CompletionRequest, error)
	parse     func(p *providerBase, state *RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error)
	stream    func(state *RequestState) streamSpec // nil = batch only
}

// dialectTable is also the matching order: first match wins, case-insensitive
// substring on the model id.
var dialectTable = []*dialect{
	{
		name:      "fim-mellum",
		role:      RoleType,
		match:     containsFold("mellum"),
		tokens:    mellumTokens,
		materials: fimMaterials,
		build:     buildFIM,
		parse:     parseFIM,
		stream:    fimStreamSpec,
	},
	{
		name:      "fim-qwen",
		role:      RoleType,
		match:     containsFold("qwen"),
		tokens:    qwenTokens,
		materials: fimMaterials,
		build:     buildFIM,
		parse:     parseFIM,
		stream:    fimStreamSpec,
	},
	{
		name:      "edit-sweep",
		role:      RoleEdit,
		match:     containsFold("sweep"),
		materials: editMaterials,
		build:     buildSweep,
		parse:     parseSweep,
		stream:    sweepStreamSpec,
	},
	{
		name:      "edit-zeta21",
		role:      RoleEdit,
		match:     anyFold("zeta-2.1", "zeta-2-1"),
		materials: editMaterials,
		build:     buildZeta21,
		parse:     parseZeta21,
	},
	{
		name:      "edit-zeta2",
		role:      RoleEdit,
		match:     containsFold("zeta"),
		materials: editMaterials,
		build:     buildZeta2,
		parse:     parseZeta2,
		stream:    zeta2StreamSpec,
	},
	{
		name:      "fim-plain",
		role:      RoleType,
		match:     func(string) bool { return true },
		materials: fimMaterials,
		build:     buildFIM,
		parse:     parseFIM,
		stream:    fimStreamSpec,
	},
}

var dialectByName = func() map[string]*dialect {
	byName := make(map[string]*dialect, len(dialectTable))
	for _, d := range dialectTable {
		byName[d.name] = d
	}
	return byName
}()

// Dialects lists every dialect name, sorted.
func Dialects() []string {
	return slices.Sorted(maps.Keys(dialectByName))
}

// dialectForModel resolves a model id through the family table.
func dialectForModel(model string) *dialect {
	for _, d := range dialectTable {
		if d.match(model) {
			return d
		}
	}
	return dialectByName["fim-plain"]
}

func containsFold(sub string) func(string) bool {
	sub = strings.ToLower(sub)
	return func(model string) bool {
		return strings.Contains(strings.ToLower(model), sub)
	}
}

func anyFold(subs ...string) func(string) bool {
	for i, sub := range subs {
		subs[i] = strings.ToLower(sub)
	}
	return func(model string) bool {
		lower := strings.ToLower(model)
		for _, sub := range subs {
			if strings.Contains(lower, sub) {
				return true
			}
		}
		return false
	}
}

// mellumTokens is the verified Mellum preset: JetBrains-style FIM tags,
// filename context headers, suffix emitted before prefix.
func mellumTokens() *types.FIMTokenConfig {
	return &types.FIMTokenConfig{
		Prefix:      "<fim_prefix>",
		Suffix:      "<fim_suffix>",
		Middle:      "<fim_middle>",
		Filename:    "<filename>",
		SuffixFirst: true,
	}
}

// qwenTokens is the standard Qwen FIM preset with repo-level context tokens.
func qwenTokens() *types.FIMTokenConfig {
	return &types.FIMTokenConfig{
		Prefix:   "<|fim_prefix|>",
		Suffix:   "<|fim_suffix|>",
		Middle:   "<|fim_middle|>",
		RepoName: "<|repo_name|>",
		FileSep:  "<|file_sep|>",
	}
}

// fimMaterials derives the FIM material set from the resolved token layout:
// Mellum renders recent file blocks, qwen repo context renders diffs and
// diagnostics too, plain prompt+suffix mode only needs the cursor scope.
func fimMaterials(cfg *types.ProviderConfig) sourcectx.Materials {
	materials := sourcectx.Materials{sourcectx.Treesitter{}}
	if tokens := cfg.FIMTokens; tokens != nil {
		switch {
		case tokens.Filename != "":
			materials = append(materials, sourcectx.RecentFiles{})
		case tokens.RepoName != "" || tokens.FileSep != "":
			materials = append(materials,
				sourcectx.GitDiff{}, sourcectx.RecentFiles{}, sourcectx.EditHistory{},
				sourcectx.Diagnostics{},
			)
		}
	}
	if cfg.RetrievalEnabled {
		materials = append(materials, sourcectx.Retrieval{})
	}
	return materials
}

// editMaterials is the shared material set for every edit dialect.
func editMaterials(*types.ProviderConfig) sourcectx.Materials {
	return sourcectx.Materials{
		sourcectx.GitDiff{}, sourcectx.RecentFiles{}, sourcectx.EditHistory{},
		sourcectx.Diagnostics{}, sourcectx.Treesitter{},
	}
}
