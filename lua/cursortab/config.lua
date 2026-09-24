-- Configuration management for cursortab.nvim

---@class CursortabUIJumpConfig
---@field symbol string
---@field text string
---@field show_distance boolean

---@class CursortabUICompletionsConfig
---@field addition_style string "dimmed" or "highlight"
---@field fg_opacity number opacity for completion overlays (0=invisible, 1=fully visible)

---@class CursortabUIConfig
---@field completions CursortabUICompletionsConfig
---@field jump CursortabUIJumpConfig

---@class CursortabCursorPredictionConfig
---@field enabled boolean
---@field auto_advance boolean
---@field proximity_threshold integer

---@class CursortabBehaviorConfig
---@field idle_completion_delay integer
---@field text_change_debounce integer
---@field max_visible_lines integer Max visible lines per completion (0 to disable)
---@field cursor_prediction CursortabCursorPredictionConfig
---@field disabled_in string[] Tree-sitter scopes where completions are suppressed (e.g., "comment", "string")
---@field ignore_paths string[] Glob patterns for files to skip (gitignore-style)
---@field ignore_filetypes string[] Filetypes to skip completions
---@field ignore_gitignored boolean Skip files matched by .gitignore
---@field enabled_modes string[] Modes where completions are active ("insert", "normal")

---@class CursortabFIMTokensConfig
---@field prefix string FIM prefix token (e.g., "<|fim_prefix|>")
---@field suffix string FIM suffix token (e.g., "<|fim_suffix|>")
---@field middle string FIM middle token (e.g., "<|fim_middle|>")
---@field repo_name string Optional repo-level FIM token (e.g., "<|repo_name|>")
---@field file_sep string Optional file separator token (e.g., "<|file_sep|>")
---@field filename string Optional filename token (e.g., "<filename>")
---@field suffix_first boolean Emit suffix content before prefix content (Mellum, SeedCoder style)

---@class CursortabMaxTokensConfig
---@field type integer Generation cap for the type model (0 = role default)
---@field edit integer Generation cap for the edit model (0 = role default)

---@class CursortabNextEditConfig
---@field model string Type model id for pause-time next-edit ("auto" picks via probe)

---@class CursortabQualityConfig
---@field logprobs boolean Request token logprobs and gate completions on their confidence
---@field min_confidence number Drop completions with mean token logprob below this (must be <= 0, 0 = off)

---@class CursortabRetrievalConfig
---@field enabled boolean Include workspace declarations that match the cursor in the prompt
---@field max_chunks integer Max retrieved code chunks per prompt (0 = default)

---@class CursortabDebugConfig
---@field immediate_shutdown boolean

---@class CursortabKeymapsConfig
---@field accept string|false Accept keymap (e.g., "<Tab>"), or false to disable
---@field partial_accept string|false Partial accept keymap (e.g., "<S-Tab>"), or false to disable
---@field trigger string|false Trigger completion keymap (e.g., "<C-Space>"), or false to disable

---@class CursortabProbeResult
---@field ok boolean
---@field count integer Number of models returned
---@field error string Failure detail when ok is false

---@class CursortabConfig
---@field url string Server base URL
---@field model string Type model id ("auto" picks via probe + family table)
---@field api_key_env string Environment variable holding the API key ("" = none)
---@field next_edit CursortabNextEditConfig|false|nil nil/false disables dual mode
---@field max_tokens CursortabMaxTokensConfig|nil Optional generation caps per role
---@field context_size integer|nil Optional context window override (nil = probe)
---@field fim_tokens CursortabFIMTokensConfig|nil Advanced: explicit FIM tokens for exotic models
---@field quality CursortabQualityConfig
---@field retrieval CursortabRetrievalConfig
---@field log_level string
---@field state_dir string Directory for runtime files (log, socket, pid)
---@field keymaps CursortabKeymapsConfig
---@field ui CursortabUIConfig
---@field behavior CursortabBehaviorConfig
---@field debug CursortabDebugConfig

-- Default configuration
---@type CursortabConfig
local default_config = {
	url = "http://localhost:8000",
	model = "auto",
	api_key_env = "",
	log_level = "info",
	state_dir = vim.fn.stdpath("state") .. "/cursortab",

	keymaps = {
		accept = "<Tab>",
		partial_accept = "<S-Tab>",
		trigger = false,
	},

	ui = {
		completions = {
			addition_style = "dimmed",
			fg_opacity = 0.6,
		},
		jump = {
			symbol = "",
			text = " TAB ",
			show_distance = true,
		},
	},

	quality = {
		logprobs = false,
		min_confidence = 0.0,
	},

	retrieval = {
		enabled = false,
		max_chunks = 0,
	},

	behavior = {
		idle_completion_delay = 400,
		text_change_debounce = 35,
		max_visible_lines = 12,
		cursor_prediction = {
			enabled = true,
			auto_advance = true,
			proximity_threshold = 3,
		},
		disabled_in = {},
		enabled_modes = { "insert", "normal" },
		ignore_paths = {
			"*.min.js",
			"*.min.css",
			"*.map",
			"*-lock.json",
			"*.lock",
			"*.sum",
			"*.csv",
			"*.tsv",
			"*.parquet",
			"*.zip",
			"*.tar",
			"*.gz",
			"*.pem",
			"*.key",
			".env",
			".env.*",
			"*.log",
		},
		ignore_filetypes = { "", "terminal" },
		ignore_gitignored = true,
	},

	debug = {
		immediate_shutdown = false,
	},
}

-- Templates for documented optional keys that stay nil in default_config, so the key validator can see inside them
local optional_templates = {
	next_edit = { model = "auto" },
	max_tokens = { type = 0, edit = 0 },
	context_size = 0,
	fim_tokens = { prefix = "", suffix = "", middle = "", repo_name = "", file_sep = "", filename = "", suffix_first = false },
}

local valid_log_levels = { trace = true, debug = true, info = true, warn = true, error = true }
local valid_addition_styles = { dimmed = true, highlight = true }
local valid_modes = { insert = true, normal = true }

---@type CursortabProbeResult|nil
local last_probe = nil

local function type_error(name, expected, value)
	error(string.format("[cursortab.nvim] %s must be %s (got %s)", name, expected, type(value)))
end

local function check_type(value, expected, name)
	if type(value) ~= expected then
		type_error(name, expected, value)
	end
end

local function check_string_list(value, name)
	check_type(value, "table", name)
	for i, item in ipairs(value) do
		if type(item) ~= "string" then
			error(string.format("[cursortab.nvim] %s[%d] must be a string", name, i))
		end
	end
end

-- Validate that all keys exist in the (template-merged) defaults
---@param user_cfg table
---@param default_cfg table
---@param path string
local function validate_config_keys(user_cfg, default_cfg, path)
	for key, value in pairs(user_cfg) do
		if default_cfg[key] == nil then
			error(string.format("[cursortab.nvim] Unknown config option: %s%s", path, key))
		end
		if type(value) == "table" and type(default_cfg[key]) == "table" and next(value) ~= nil and type(next(value)) ~= "number" then
			validate_config_keys(value, default_cfg[key], path .. key .. ".")
		end
	end
end

local function validate_keymap(value, name)
	if value == nil then
		return
	end
	if value ~= false and type(value) ~= "string" then
		error(string.format("[cursortab.nvim] %s must be a string (keymap) or false to disable", name))
	end
	if value == "" then
		error(string.format("[cursortab.nvim] %s cannot be an empty string (use false to disable)", name))
	end
end

-- Validate configuration values: types and enums only.
-- Numeric ranges are validated by the Go daemon at startup.
---@param cfg table Merged configuration
local function validate_values(cfg)
	check_type(cfg.url, "string", "url")
	if cfg.url == "" then
		error("[cursortab.nvim] url is required")
	end
	check_type(cfg.model, "string", "model")
	check_type(cfg.api_key_env, "string", "api_key_env")
	check_type(cfg.log_level, "string", "log_level")
	if not valid_log_levels[cfg.log_level] then
		error(string.format("[cursortab.nvim] Invalid log_level '%s'. Must be one of: trace, debug, info, warn, error", cfg.log_level))
	end
	check_type(cfg.state_dir, "string", "state_dir")

	validate_keymap(cfg.keymaps.accept, "keymaps.accept")
	validate_keymap(cfg.keymaps.partial_accept, "keymaps.partial_accept")
	validate_keymap(cfg.keymaps.trigger, "keymaps.trigger")

	if cfg.next_edit ~= nil and cfg.next_edit ~= false then
		check_type(cfg.next_edit, "table", "next_edit")
		if cfg.next_edit.model ~= nil then
			check_type(cfg.next_edit.model, "string", "next_edit.model")
		end
	end

	if cfg.max_tokens ~= nil then
		check_type(cfg.max_tokens, "table", "max_tokens")
		if cfg.max_tokens.type ~= nil then
			check_type(cfg.max_tokens.type, "number", "max_tokens.type")
		end
		if cfg.max_tokens.edit ~= nil then
			check_type(cfg.max_tokens.edit, "number", "max_tokens.edit")
		end
	end

	if cfg.context_size ~= nil then
		check_type(cfg.context_size, "number", "context_size")
	end

	if cfg.fim_tokens ~= nil then
		check_type(cfg.fim_tokens, "table", "fim_tokens (preset names are gone, pass a token table)")
		for _, field in ipairs({ "prefix", "suffix", "middle" }) do
			local value = cfg.fim_tokens[field]
			if value == nil or type(value) ~= "string" or value == "" then
				error(string.format("[cursortab.nvim] fim_tokens.%s is required and must be a non-empty string", field))
			end
		end
		for _, field in ipairs({ "repo_name", "file_sep", "filename" }) do
			if cfg.fim_tokens[field] ~= nil then
				check_type(cfg.fim_tokens[field], "string", "fim_tokens." .. field)
			end
		end
		if cfg.fim_tokens.suffix_first ~= nil then
			check_type(cfg.fim_tokens.suffix_first, "boolean", "fim_tokens.suffix_first")
		end
	end

	check_type(cfg.quality.logprobs, "boolean", "quality.logprobs")
	check_type(cfg.quality.min_confidence, "number", "quality.min_confidence")

	check_type(cfg.retrieval.enabled, "boolean", "retrieval.enabled")
	check_type(cfg.retrieval.max_chunks, "number", "retrieval.max_chunks")

	check_type(cfg.ui.completions.addition_style, "string", "ui.completions.addition_style")
	if not valid_addition_styles[cfg.ui.completions.addition_style] then
		error(string.format(
			"[cursortab.nvim] Invalid ui.completions.addition_style '%s'. Must be one of: dimmed, highlight",
			cfg.ui.completions.addition_style
		))
	end
	local f = cfg.ui.completions.fg_opacity
	if type(f) ~= "number" or f < 0 or f > 1 then
		error("[cursortab.nvim] ui.completions.fg_opacity must be a number between 0 and 1")
	end
	check_type(cfg.ui.jump.symbol, "string", "ui.jump.symbol")
	check_type(cfg.ui.jump.text, "string", "ui.jump.text")
	check_type(cfg.ui.jump.show_distance, "boolean", "ui.jump.show_distance")

	check_type(cfg.behavior.idle_completion_delay, "number", "behavior.idle_completion_delay")
	check_type(cfg.behavior.text_change_debounce, "number", "behavior.text_change_debounce")
	check_type(cfg.behavior.max_visible_lines, "number", "behavior.max_visible_lines")
	check_type(cfg.behavior.disabled_in, "table", "behavior.disabled_in")
	check_string_list(cfg.behavior.ignore_paths, "behavior.ignore_paths")
	check_string_list(cfg.behavior.ignore_filetypes, "behavior.ignore_filetypes")
	check_type(cfg.behavior.ignore_gitignored, "boolean", "behavior.ignore_gitignored")
	check_type(cfg.behavior.enabled_modes, "table", "behavior.enabled_modes")
	for i, mode in ipairs(cfg.behavior.enabled_modes) do
		if type(mode) ~= "string" or not valid_modes[mode] then
			error(string.format(
				"[cursortab.nvim] behavior.enabled_modes[%d] = %q is invalid. Must be \"insert\" or \"normal\"",
				i,
				tostring(mode)
			))
		end
	end
	local cp = cfg.behavior.cursor_prediction
	check_type(cp, "table", "behavior.cursor_prediction")
	check_type(cp.enabled, "boolean", "behavior.cursor_prediction.enabled")
	check_type(cp.auto_advance, "boolean", "behavior.cursor_prediction.auto_advance")
	check_type(cp.proximity_threshold, "number", "behavior.cursor_prediction.proximity_threshold")

	check_type(cfg.debug.immediate_shutdown, "boolean", "debug.immediate_shutdown")
end

---@class ConfigModule
local config = {}
---@type CursortabConfig
local current_config = vim.deepcopy(default_config)

-- Get current configuration
---@return CursortabConfig
function config.get()
	return current_config
end

-- Resolve the configured api_key_env into an API key ("" when unset).
---@return string
function config.resolve_api_key()
	local env = current_config.api_key_env
	if not env or env == "" then
		return ""
	end
	local value = vim.fn.getenv(env)
	if value == vim.NIL or value == "" then
		return ""
	end
	return value
end

-- Store a model selection from :Cursortab model, an edit-role pick enables dual mode when it was off
---@param role "type"|"edit"
---@param id string
function config.set_model(role, id)
	if role == "type" then
		current_config.model = id
		return
	end
	if type(current_config.next_edit) ~= "table" then
		current_config.next_edit = { model = id }
	else
		current_config.next_edit.model = id
	end
end

-- Record the outcome of the last :Cursortab model probe for checkhealth.
---@param result CursortabProbeResult|nil
function config.set_probe_result(result)
	last_probe = result
end

---@return CursortabProbeResult|nil
function config.get_probe_result()
	return last_probe
end

-- Set up configuration with user overrides
---@param user_config table|nil User configuration overrides
---@return CursortabConfig
function config.setup(user_config)
	local migrated = user_config or {}
	local validation_defaults = vim.tbl_deep_extend("force", vim.deepcopy(default_config), vim.deepcopy(optional_templates))
	validate_config_keys(migrated, validation_defaults, "")

	local merged = vim.tbl_deep_extend("force", vim.deepcopy(default_config), migrated)
	-- next_edit tables given without a model inherit the auto-pick default
	if type(merged.next_edit) == "table" and merged.next_edit.model == nil then
		merged.next_edit.model = "auto"
	end
	validate_values(merged)
	current_config = merged

	return current_config
end

-- Set up default values for highlight groups
function config.setup_highlights()
	vim.api.nvim_set_hl(0, "CursorTabDeletion", {
		default = true,
		ctermbg = "DarkRed",
		bg = "#4f2f2f",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabAddition", {
		default = true,
		ctermbg = "DarkGreen",
		bg = "#394f2f",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabModification", {
		default = true,
		ctermbg = "DarkGray",
		bg = "#282e38",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabCompletion", {
		default = true,
		ctermfg = "DarkBlue",
		fg = "#80899c",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabJumpSymbol", {
		default = true,
		ctermfg = "Cyan",
		fg = "#373b45",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabJumpText", {
		default = true,
		ctermbg = "Cyan",
		ctermfg = "Black",
		bg = "#373b45",
		fg = "#bac1d1",
		bold = false,
	})
end

return config
