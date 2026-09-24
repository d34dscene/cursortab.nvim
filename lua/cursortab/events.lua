-- Event handling and autocommands for cursortab.nvim

local buffer = require("cursortab.buffer")
local config = require("cursortab.config")
local daemon = require("cursortab.daemon")
local ui = require("cursortab.ui")

---@class EventsModule
local events = {}

-- Check if a mode is enabled in the config
---@param mode string "insert" or "normal"
---@return boolean
local function is_mode_enabled(mode)
	local modes = config.get().behavior.enabled_modes
	for _, m in ipairs(modes) do
		if m == mode then
			return true
		end
	end
	return false
end

-- Track currently bound keys so we can clean them up on re-setup
---@type {accept: string|nil, partial_accept: string|nil, trigger: string|nil}
local current_keymaps = { accept = nil, partial_accept = nil, trigger = nil }

local esc_handler_ns = nil

-- Buffer whose mirror Go holds, plus which buffers have live attach callbacks
---@type integer|nil
local active_buf = nil
---@type table<integer, true>
local attached = {}
-- Tick of the last pushed text event. Anything that advances changedtick
-- without a push (skipped echoes, undo phantoms, disconnects) leaves a gap
-- that the next text_changed or TextChanged closes with a full payload.
local last_sent_tick = -1

-- Whether blink-cmp is installed (detected once)
local has_blink = pcall(require, "blink.cmp")

-- Suppress blink-cmp via its buffer-local variable (safe in expr mappings)
local function suppress_blink()
	if has_blink then
		vim.b.completion = false
	end
end

-- Re-enable blink-cmp
local function release_blink()
	if has_blink and vim.b.completion == false then
		vim.b.completion = nil
	end
end

-- Close any visible native completion menus. Must run via vim.schedule (not safe in expr mappings).
local function dismiss_native_completion()
	if vim.fn.pumvisible() == 1 then
		local keys = vim.api.nvim_replace_termcodes("<C-e>", true, false, true)
		vim.api.nvim_feedkeys(keys, "n", false)
	end
	local ok_cmp, cmp = pcall(require, "cmp")
	if ok_cmp and type(cmp.visible) == "function" and cmp.visible() then
		cmp.abort()
	end
	local ok_blink, blink = pcall(require, "blink.cmp")
	if ok_blink and blink.is_visible and blink.is_visible() then
		blink.cancel()
	end
end

-- Single in-flight table replacing the old one-shot booleans, with a deadline so no value can strand. kind is accept/partial/jump while Go applies, text for a just-sent delta absorbing its cursor echo.
---@type {seq: integer, deadline: integer, kind: string}|nil
local pending = nil
local pending_seq = 0

local ACCEPT_WINDOW_MS = 5000
local TEXT_WINDOW_MS = 200

local function set_pending(kind, window_ms)
	pending_seq = pending_seq + 1
	local seq = pending_seq
	pending = { seq = seq, deadline = vim.uv.now() + window_ms, kind = kind }
	vim.defer_fn(function()
		if pending and pending.seq == seq then
			pending = nil
		end
		release_blink()
	end, window_ms)
	return seq
end

local function pending_active()
	if pending and vim.uv.now() >= pending.deadline then
		pending = nil
	end
	return pending
end

local function full_extra()
	if not active_buf or not vim.api.nvim_buf_is_valid(active_buf) then
		return nil
	end
	return { full = { lines = vim.api.nvim_buf_get_lines(active_buf, 0, -1, false) } }
end

-- Send a text_changed event carrying either a changed range or a full snapshot
---@param extra {changed: table}|{full: table}|nil
---@return boolean
local function send_text(extra)
	if not daemon.is_enabled() or not daemon.is_connected() then
		return false
	end
	local payload = daemon.build_payload(active_buf, extra)
	if not daemon.send_payload("text_changed", payload) then
		return false
	end
	last_sent_tick = payload.tick
	return true
end

-- Send a non-text event with cursor/viewport keys for the active buffer
---@param name string
---@return boolean
local function send(name)
	if buffer.should_skip() or not daemon.is_enabled() then
		return false
	end
	return daemon.send_payload(name, daemon.build_payload(active_buf, nil))
end

local attach ---@type fun(bufnr: integer)

local function text_change_allowed()
	local mode = vim.api.nvim_get_mode().mode:sub(1, 1)
	if mode == "i" then
		return is_mode_enabled("insert")
	end
	return is_mode_enabled("normal")
end

attach = function(bufnr)
	if attached[bufnr] then
		return
	end
	attached[bufnr] = true
	local ok = vim.api.nvim_buf_attach(bufnr, false, {
		-- One delta per change. Go-initiated edits while an accept is pending are not echoed, the next text_changed carries a full instead.
		on_lines = function(_, b, tick, first, last_old, last_new)
			if b ~= active_buf then
				return false
			end
			if not daemon.is_enabled() or not daemon.is_connected() then
				return false
			end
			local p = pending_active()
			if p and p.kind ~= "text" then
				return false
			end
			if not text_change_allowed() then
				return false
			end
			local sent = false
			if tick ~= last_sent_tick + 1 then
				local extra = full_extra()
				if extra then
					sent = send_text(extra)
				end
			else
				sent = send_text({
					changed = {
						first = first,
						last_old = last_old,
						last_new = last_new,
						lines = vim.api.nvim_buf_get_lines(b, first, last_new, false),
					},
				})
			end
			if sent then
				-- Arm only here: this change's CursorMoved echo still fires after on_lines
				set_pending("text", TEXT_WINDOW_MS)
			end
			return false
		end,
		-- Fired on buffer reloads like :e which silently kill the callbacks, re-attach and resend a full for the active buffer.
		on_detach = function(_, b)
			attached[b] = nil
			vim.schedule(function()
				if not vim.api.nvim_buf_is_valid(b) then
					if active_buf == b then
						active_buf = nil
					end
					return
				end
				if not vim.api.nvim_buf_is_loaded(b) then
					return
				end
				attach(b)
				if active_buf == b then
					local extra = full_extra()
					if extra then
						send_text(extra)
					end
				end
			end)
		end,
	})
	if not ok then
		attached[bufnr] = nil
	end
end

local function activate_if_possible()
	if buffer.should_skip() then
		return
	end
	local bufnr = vim.api.nvim_get_current_buf()
	attach(bufnr)
	if active_buf ~= bufnr then
		active_buf = bufnr
		last_sent_tick = -1
		local extra = full_extra()
		if extra then
			send_text(extra)
		end
	end
end

-- Accept key handler
---@return string
local function on_accept()
	if ui.has_cursor_prediction() or ui.has_completion() then
		set_pending(ui.has_cursor_prediction() and "jump" or "accept", ACCEPT_WINDOW_MS)
		suppress_blink()
		vim.schedule(function()
			dismiss_native_completion()
			release_blink()
		end)
		send("accept")
		return ""
	end
	return "\t"
end

-- Escape key handler, only meaningful while something is displayed (§6.6)
local function on_escape()
	if ui.has_completion() or ui.has_cursor_prediction() then
		send("esc")
	end
end

-- Partial accept handler (Shift-Tab by default)
---@return string
local function on_partial_accept()
	if ui.has_completion() then
		set_pending("partial", ACCEPT_WINDOW_MS)
		suppress_blink()
		vim.schedule(function()
			dismiss_native_completion()
			release_blink()
		end)
		send("partial_accept")
		return ""
	end
	local cfg = config.get()
	return vim.api.nvim_replace_termcodes(cfg.keymaps.partial_accept, true, true, true)
end

-- Manual trigger handler
local function on_trigger()
	send("trigger_completion")
end

-- Shared cursor movement handler (UI only, does not send event)
---@return boolean suppressed true if the event was suppressed (skip sending)
local function handle_cursor_moved()
	local p = pending_active()
	if p then
		-- Text echoes are consumed once, accept-family states persist until TextChanged, an RPC answer, or their deadline.
		if p.kind == "text" then
			pending = nil
		end
		return true
	end
	if ui.has_cursor_prediction() or ui.has_completion() then
		ui.ensure_close_all()
	end
	return false
end

-- Update a single keymap slot: clear old binding if changed, set new one
local function update_keymap(name, new_key, handler, opts)
	if current_keymaps[name] and current_keymaps[name] ~= new_key then
		pcall(vim.keymap.del, "i", current_keymaps[name])
		pcall(vim.keymap.del, "n", current_keymaps[name])
		current_keymaps[name] = nil
	end
	if new_key then
		vim.keymap.set("i", new_key, handler, opts)
		vim.keymap.set("n", new_key, handler, opts)
		current_keymaps[name] = new_key
	end
end

-- Set up keymaps (can be called multiple times when config changes)
local function setup_keymaps()
	local cfg = config.get()
	local expr_opts = { noremap = true, silent = true, expr = true }
	local plain_opts = { noremap = true, silent = true }

	update_keymap("accept", cfg.keymaps.accept, on_accept, expr_opts)
	update_keymap("partial_accept", cfg.keymaps.partial_accept, on_partial_accept, expr_opts)
	update_keymap("trigger", cfg.keymaps.trigger, on_trigger, plain_opts)

	if esc_handler_ns then
		vim.on_key(nil, esc_handler_ns)
	end

	local ESC = vim.keycode("<Esc>")
	esc_handler_ns = vim.on_key(function(_, typed)
		if typed == ESC then
			on_escape()
		end
	end)
end

-- Set up autocommands and buffer tracking
local function setup_autocommands()
	local augroup = vim.api.nvim_create_augroup("cursortab", { clear = true })

	-- Track focus changes, a new buffer becomes the mirror target and gets an immediate full snapshot
	vim.api.nvim_create_autocmd({ "BufEnter", "WinEnter" }, {
		group = augroup,
		callback = function(args)
			vim.schedule(function()
				buffer.update_state()
				if args.event == "BufEnter" then
					activate_if_possible()
				end
			end)
		end,
	})

	vim.api.nvim_create_autocmd({ "TextChanged", "TextChangedI" }, {
		group = augroup,
		callback = function(args)
			-- Any text change ends the waiting state, whatever started it
			pending = nil

			if buffer.should_skip() then
				return
			end

			-- Keep ghost visuals coherent while the user types into them
			if ui.has_cursor_prediction() then
				ui.ensure_close_all()
			elseif ui.has_completion() then
				local current_line = vim.api.nvim_get_current_line()
				local cursor_line = vim.fn.line(".")
				if ui.typing_matches_completion(cursor_line, current_line) then
					ui.update_ghost_text_for_typing(cursor_line, current_line)
				else
					ui.ensure_close_all()
				end
			end

			local mode_ok = args.event == "TextChangedI" and is_mode_enabled("insert") or is_mode_enabled("normal")
			if not mode_ok then
				return
			end
			-- Resend a full snapshot when changedtick moved past what we pushed (skipped echoes, undo phantoms, reconnect gaps)
			if active_buf and vim.api.nvim_buf_is_valid(active_buf) and vim.api.nvim_buf_get_changedtick(active_buf) ~= last_sent_tick then
				local extra = full_extra()
				if extra then
					send_text(extra)
				end
			end
		end,
	})

	-- Cursor movement events (normal mode)
	vim.api.nvim_create_autocmd({ "CursorMoved" }, {
		group = augroup,
		callback = function()
			local mode = vim.api.nvim_get_mode().mode:sub(1, 1)
			if mode ~= "n" then
				return
			end
			if handle_cursor_moved() then
				return
			end
			if not is_mode_enabled("normal") then
				return
			end
			send("cursor_moved")
		end,
	})

	-- Cursor movement events (insert mode - e.g., arrow keys)
	vim.api.nvim_create_autocmd({ "CursorMovedI" }, {
		group = augroup,
		callback = function()
			if handle_cursor_moved() then
				return
			end
			if not is_mode_enabled("insert") then
				return
			end
			send("cursor_moved")
		end,
	})

	vim.api.nvim_create_autocmd({ "InsertEnter" }, {
		group = augroup,
		callback = function()
			send("insert_enter")
		end,
	})

	vim.api.nvim_create_autocmd({ "InsertLeave" }, {
		group = augroup,
		callback = function()
			if buffer.should_skip() then
				return
			end
			if ui.has_cursor_prediction() or ui.has_completion() then
				ui.ensure_close_all()
			end
			send("insert_leave")
		end,
	})

	-- File save: reset diff history baseline
	vim.api.nvim_create_autocmd({ "BufWritePost" }, {
		group = augroup,
		callback = function()
			send("file_saved")
		end,
	})

	-- Close completions/predictions on context switches. Rejects go out only
	-- while something is displayed, and never mid-accept.
	vim.api.nvim_create_autocmd({ "ModeChanged", "CmdlineEnter", "CmdwinEnter", "BufEnter" }, {
		group = augroup,
		callback = function(args)
			-- Don't close when transitioning from normal to insert mode
			if args.event == "ModeChanged" and args.match and args.match:match("^n:i") then
				return
			end

			local p = pending_active()
			if p and p.kind ~= "text" then
				return
			end

			if not (ui.has_completion() or ui.has_cursor_prediction()) then
				return
			end
			ui.ensure_close_all()
			send("esc")
		end,
	})
end

-- Set up all autocommands and keymaps
function events.setup()
	buffer.setup()
	setup_autocommands()
	setup_keymaps()
	daemon.on_connected = events.resync
	activate_if_possible()
end

-- Reply to Go's cursortab_resync request (and reconnect resync) with a full payload
function events.resync()
	if not active_buf or not vim.api.nvim_buf_is_valid(active_buf) then
		active_buf = nil
		if buffer.should_skip() then
			return
		end
		active_buf = vim.api.nvim_get_current_buf()
		attach(active_buf)
		last_sent_tick = -1
	end
	local extra = full_extra()
	if extra then
		send_text(extra)
	end
end

-- Clear all completions (exposed for manual use)
function events.clear_all_completions()
	local shown = ui.has_completion() or ui.has_cursor_prediction()
	pending = nil
	ui.close_all()
	if shown then
		send("esc")
	end
end

-- Clear the in-flight pending state (called on every RPC answer)
function events.clear_pending()
	pending = nil
end

---Accept current completion/prediction if available.
---@return boolean accepted
function events.accept()
	return on_accept() == ""
end

---Check if cursortab is mid-completion (for other plugins to suppress their menus).
---@return boolean
function events.is_completing()
	return pending_active() ~= nil
end

return events
