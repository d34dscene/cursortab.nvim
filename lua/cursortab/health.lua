local config = require("cursortab.config")
local daemon = require("cursortab.daemon")

local M = {}

function M.check()
	local cfg = config.get()
	local daemon_status = daemon.check_daemon_status()
	local connection = daemon.get_connection_status()

	-- Identity
	vim.health.start("Identity")
	local nv = vim.version()
	vim.health.info(
		"neovim: "
			.. string.format("%d.%d.%d", nv.major, nv.minor, nv.patch)
			.. " ("
			.. vim.uv.os_uname().sysname ---@diagnostic disable-line: undefined-field
			.. ")"
	)

	-- Daemon
	vim.health.start("Daemon")
	local binary_version = daemon.get_binary_version()
	if binary_version then
		vim.health.info("binary_version: " .. binary_version)
	else
		vim.health.warn("binary_version: unknown")
	end
	if not daemon.is_enabled() then
		vim.health.warn("Plugin is disabled (:CursortabToggle)")
	elseif daemon_status.daemon_running and connection.connected then
		vim.health.ok("Running (pid: " .. daemon_status.pid .. ", channel: " .. connection.channel_id .. ")")
	elseif daemon_status.daemon_running then
		vim.health.warn("Process running (pid: " .. daemon_status.pid .. ") but not connected")
	else
		vim.health.error("Not running", { "Run :CursortabRestart to start the daemon" })
	end

	-- Connection and reconnect state
	vim.health.start("Connection")
	if connection.degraded then
		vim.health.error(
			"Degraded: gave up after " .. connection.reconnect_attempts .. " reconnect attempts",
			{ "Last error: " .. (connection.last_error ~= "" and connection.last_error or "unknown"), "Run :CursortabRestart" }
		)
	elseif connection.reconnecting then
		vim.health.warn(
			"Reconnecting (attempt " .. connection.reconnect_attempts .. " of 5): " .. connection.last_error
		)
	elseif connection.connected then
		vim.health.ok("Connected")
	else
		vim.health.warn("Not connected yet")
	end

	-- Probe
	vim.health.start("Probe")
	local probe = config.get_probe_result()
	if not probe then
		vim.health.info("No probe yet (run :Cursortab model)")
	elseif probe.ok then
		vim.health.ok(probe.count .. " models available")
	else
		vim.health.error("Last probe failed: " .. probe.error, { "Check url and that the server is running" })
	end

	-- Models and endpoint
	vim.health.start("Models")
	vim.health.info("url: " .. cfg.url)
	vim.health.info("type model: " .. cfg.model)
	if type(cfg.next_edit) == "table" then
		vim.health.info("edit model: " .. (cfg.next_edit.model or "auto") .. " (dual mode on)")
	else
		vim.health.info("edit model: - (single mode)")
	end
	if cfg.api_key_env ~= "" then
		local key = vim.fn.getenv(cfg.api_key_env)
		if key == vim.NIL or key == "" then
			vim.health.error(cfg.api_key_env .. " is not set", {
				"Export " .. cfg.api_key_env .. " in your shell config",
				"Run :CursortabRestart after setting it",
			})
		else
			vim.health.ok(cfg.api_key_env .. " is set")
		end
	end
	vim.health.info("context_size: " .. (cfg.context_size and tostring(cfg.context_size) or "auto (probe)"))
	if cfg.max_tokens then
		vim.health.info(
			"max_tokens: type=" .. tostring(cfg.max_tokens.type or 0) .. " edit=" .. tostring(cfg.max_tokens.edit or 0)
		)
	end
	if cfg.fim_tokens then
		vim.health.info("fim_tokens: explicit (prefix/suffix/middle set)")
	else
		vim.health.info("fim_tokens: dialect presets")
	end
	vim.health.info("quality.logprobs: " .. (cfg.quality.logprobs and "yes" or "no"))
	vim.health.info("quality.min_confidence: " .. cfg.quality.min_confidence)
	vim.health.info("retrieval: " .. (cfg.retrieval.enabled and ("on, max_chunks=" .. cfg.retrieval.max_chunks) or "off"))

	-- Behavior
	vim.health.start("Behavior")
	vim.health.info("idle_completion_delay: " .. cfg.behavior.idle_completion_delay .. "ms")
	vim.health.info("text_change_debounce: " .. cfg.behavior.text_change_debounce .. "ms")
	vim.health.info("max_visible_lines: " .. cfg.behavior.max_visible_lines)
	vim.health.info("cursor_prediction: " .. (cfg.behavior.cursor_prediction.enabled and "yes" or "no"))
	vim.health.info("auto_advance: " .. (cfg.behavior.cursor_prediction.auto_advance and "yes" or "no"))
	vim.health.info("proximity_threshold: " .. cfg.behavior.cursor_prediction.proximity_threshold)
	vim.health.info("enabled_modes: " .. table.concat(cfg.behavior.enabled_modes, ", "))
	vim.health.info("disabled_in: " .. (#cfg.behavior.disabled_in > 0 and table.concat(cfg.behavior.disabled_in, ", ") or "-"))
	vim.health.info("ignore_paths: " .. #cfg.behavior.ignore_paths .. " patterns")
	vim.health.info("ignore_filetypes: " .. #cfg.behavior.ignore_filetypes .. " filetypes")
	vim.health.info("ignore_gitignored: " .. (cfg.behavior.ignore_gitignored and "yes" or "no"))

	-- Keymaps
	vim.health.start("Keymaps")
	vim.health.info("accept: " .. (cfg.keymaps.accept or "disabled"))
	vim.health.info("partial_accept: " .. (cfg.keymaps.partial_accept or "disabled"))
	vim.health.info("trigger: " .. (cfg.keymaps.trigger or "disabled"))

	-- UI
	vim.health.start("UI")
	vim.health.info("addition_style: " .. cfg.ui.completions.addition_style)
	vim.health.info("fg_opacity: " .. cfg.ui.completions.fg_opacity)
	vim.health.info("jump_symbol: " .. cfg.ui.jump.symbol)
	vim.health.info("jump_text: " .. cfg.ui.jump.text)
	vim.health.info("jump_show_distance: " .. (cfg.ui.jump.show_distance and "yes" or "no"))

	-- Paths
	vim.health.start("Paths")
	vim.health.info("enabled: " .. (daemon.is_enabled() and "yes" or "no"))
	vim.health.info("state_dir: " .. cfg.state_dir)
	vim.health.info("log_level: " .. cfg.log_level)
	vim.health.info("immediate_shutdown: " .. (cfg.debug.immediate_shutdown and "yes" or "no"))
end

return M
