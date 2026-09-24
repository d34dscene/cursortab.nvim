-- Main entry point for cursortab.nvim
-- Import all modules
local config = require("cursortab.config")
local daemon = require("cursortab.daemon")
local events = require("cursortab.events")
local ui = require("cursortab.ui")

---@class CursortabModule
local M = {}

-- RPC callback functions (called from Go daemon)
-- These must remain globally accessible for the RPC interface

---RPC callback: called when completion is rejected
function M.on_reject()
	events.clear_pending()
	ui.close_all()
end

---Accept current completion/prediction if available.
---@return boolean accepted
function M.accept()
	return events.accept()
end

---Check if cursortab is mid-completion (for other plugins to suppress their menus).
---@return boolean
function M.is_completing()
	return events.is_completing()
end

---RPC callback: called when completion is ready
---@param diff_result DiffResult Completion diff result from Go daemon
function M.on_completion_ready(diff_result)
	events.clear_pending()
	ui.show_completion(diff_result)
end

---RPC callback: called when cursor prediction is ready
---@param line_num integer Predicted line number (1-indexed)
function M.on_cursor_prediction_ready(line_num)
	events.clear_pending()
	ui.show_cursor_prediction(line_num)
end

---RPC callback: Go asks for a full snapshot of the active buffer (§4 cursortab_resync)
function M.on_resync()
	events.resync()
end

-- Public API functions for users

---Toggle cursortab functionality on/off
function M.toggle()
	local enabled = not daemon.is_enabled()
	daemon.set_enabled(enabled)

	if enabled then
		vim.notify("Cursortab enabled", vim.log.levels.INFO)
	else
		vim.notify("Cursortab disabled", vim.log.levels.INFO)
		-- Clear all completions and predictions when disabling
		events.clear_all_completions()
	end
end

---Show cursortab log file in a floating window
function M.show_log()
	local cfg = config.get()
	local log_path = cfg.state_dir .. "/cursortab.log"

	-- Check if log file exists
	if vim.fn.filereadable(log_path) == 0 then
		vim.notify("Log file not found: " .. log_path, vim.log.levels.WARN)
		return
	end

	-- Read the log file content
	local lines = vim.fn.readfile(log_path)

	-- Create scratch window using UI module
	ui.create_scratch_window("Cursortab Log", lines, {
		filetype = "log",
		move_to_end = true,
		size_mode = "fullscreen",
	})

	vim.notify("Showing cursortab log", vim.log.levels.INFO)
end

---Clear cursortab log file
function M.clear_log()
	local cfg = config.get()
	local log_path = cfg.state_dir .. "/cursortab.log"

	-- Check if log file exists
	if vim.fn.filereadable(log_path) == 0 then
		vim.notify("Log file not found: " .. log_path, vim.log.levels.WARN)
		return
	end

	-- Clear the log file by writing empty content
	vim.fn.writefile({}, log_path)

	vim.notify("Cursortab log cleared", vim.log.levels.INFO)
end

---Show cursortab status via checkhealth
function M.status()
	vim.cmd("checkhealth cursortab")
end

---Restart cursortab daemon (never blocks the UI)
function M.restart()
	vim.notify("Restarting cursortab daemon...", vim.log.levels.INFO)
	events.clear_all_completions()
	daemon.stop_daemon(function()
		if daemon.force_start() then
			vim.notify("Cursortab daemon restarted successfully", vim.log.levels.INFO)
		else
			vim.notify("Failed to start cursortab daemon", vim.log.levels.ERROR)
		end
	end)
end

-- Role classification mirrors the server dialect table first-match order (mellum/qwen are type, sweep/zeta are edit)
---@param id string
---@return "type"|"edit"
local function classify_model_role(id)
	local s = id:lower()
	if s:find("mellum", 1, true) or s:find("qwen", 1, true) then
		return "type"
	end
	if s:find("sweep", 1, true) or s:find("zeta", 1, true) then
		return "edit"
	end
	return "type"
end

---Probe {url}/v1/models, select models per role, store them, restart the daemon
function M.model()
	local cfg = config.get()
	local args = { "curl", "-sS", "--max-time", "5", "-H", "Accept: application/json" }
	local api_key = config.resolve_api_key()
	if api_key ~= "" then
		table.insert(args, "-H")
		table.insert(args, "Authorization: Bearer " .. api_key)
	end
	table.insert(args, cfg.url .. "/v1/models")
	vim.system(args, { text = true }, function(result)
		vim.schedule(function()
			M._handle_models(result)
		end)
	end)
end

---Handle the async probe result, store it for checkhealth and open the picker
---@param result {code: integer, stdout: string, stderr: string}
function M._handle_models(result)
	local cfg = config.get()

	local function fail(detail)
		config.set_probe_result({ ok = false, count = 0, error = detail })
		vim.notify("cursortab: model probe failed: " .. detail, vim.log.levels.ERROR)
	end

	if result.code ~= 0 then
		fail(vim.trim(result.stderr ~= "" and result.stderr or ("curl exited with code " .. result.code)))
		return
	end

	local ok, decoded = pcall(vim.json.decode, result.stdout or "")
	local data = ok and type(decoded) == "table" and decoded.data or nil
	if type(data) ~= "table" then
		fail("invalid response from " .. cfg.url .. "/v1/models")
		return
	end

	local items = {}
	for _, entry in ipairs(data) do
		local id = type(entry) == "table" and entry.id or nil
		if type(id) == "string" and id ~= "" then
			local role = classify_model_role(id)
			table.insert(items, { role = role, id = id, label = "[" .. role .. "] " .. id })
		end
	end
	if #items == 0 then
		fail("no models returned by " .. cfg.url)
		return
	end

	table.sort(items, function(a, b)
		if a.role ~= b.role then
			return a.role == "type"
		end
		return a.id < b.id
	end)
	config.set_probe_result({ ok = true, count = #items, error = "" })

	vim.ui.select(items, {
		prompt = "Cursortab models (" .. cfg.url .. ")",
		format_item = function(item)
			return item.label
		end,
	}, function(choice)
		if not choice then
			return
		end
		config.set_model(choice.role, choice.id)
		vim.notify("cursortab: " .. choice.role .. " model set to " .. choice.id, vim.log.levels.INFO)
		M.restart()
	end)
end

---Setup cursortab with user configuration
---@param user_config table|nil User configuration overrides
function M.setup(user_config)
	-- Setup configuration
	config.setup(user_config)

	-- Create user commands
	vim.api.nvim_create_user_command("CursortabToggle", function()
		M.toggle()
	end, { desc = "Toggle Cursortab functionality" })

	vim.api.nvim_create_user_command("CursortabShowLog", function()
		M.show_log()
	end, { desc = "Show cursortab log file in a scratch window" })

	vim.api.nvim_create_user_command("CursortabClearLog", function()
		M.clear_log()
	end, { desc = "Clear cursortab log file" })

	vim.api.nvim_create_user_command("CursortabStatus", function()
		M.status()
	end, { desc = "Show cursortab status information" })

	vim.api.nvim_create_user_command("CursortabRestart", function()
		M.restart()
	end, { desc = "Restart cursortab daemon" })

	vim.api.nvim_create_user_command("Cursortab", function(cmd)
		if cmd.args == "model" then
			M.model()
		else
			vim.notify("Usage: :Cursortab model", vim.log.levels.INFO)
		end
	end, {
		nargs = "?",
		complete = function()
			return { "model" }
		end,
		desc = "Cursortab subcommands (:Cursortab model)",
	})

	-- Setup highlight groups
	config.setup_highlights()

	-- Set up highlight namespace
	vim.api.nvim_set_hl_ns(daemon.get_namespace_id())

	-- Setup events and autocommands
	events.setup()

	-- Patch completion plugins after all plugins have loaded
	vim.schedule(function()
		M._patch_completion_plugins()
	end)

	-- Start the daemon (non-blocking)
	vim.defer_fn(function()
		daemon.force_start()
	end, 0)
end

-- Wrap a completion plugin's enabled function to return false while cursortab is completing
---@param original any
---@return function
local function wrap_enabled(original)
	return function()
		if events.is_completing() then
			return false
		end
		if type(original) == "function" then
			return original()
		end
		if original == nil then
			return true
		end
		return original
	end
end

-- Patch nvim-cmp to suppress during cursortab completion
function M._patch_completion_plugins()
	local ok_cmp, cmp = pcall(require, "cmp")
	if ok_cmp and cmp.get_config and vim.is_callable(cmp.setup) then
		local cmp_config = cmp.get_config()
		cmp.setup({ enabled = wrap_enabled(cmp_config.enabled) })
	end
end

return M
