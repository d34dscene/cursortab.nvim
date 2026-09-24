-- Daemon management and RPC communication for cursortab.nvim

local config = require("cursortab.config")

local daemon = {}

-- Module state
---@type integer|nil
local chan = nil
local ns_id = vim.api.nvim_create_namespace("cursortab")
local is_enabled = true
local stopping = false

-- Last built process environment (CURSORTAB_CONFIG), reused by reconnects
---@type table|nil
local last_env = nil
---@type string|nil
local last_config_json = nil

local is_windows = vim.fn.has("win32") == 1 or vim.fn.has("win64") == 1

local MAX_RECONNECT_ATTEMPTS = 5
---@type {attempt: integer, waiting: boolean, degraded: boolean, last_error: string}
local reconnect = { attempt = 0, waiting = false, degraded = false, last_error = "" }

---@type fun()|nil
local schedule_reconnect

local function get_ipc_path(state_dir)
	if is_windows then
		return state_dir .. "/cursortab.port"
	else
		return state_dir .. "/cursortab.sock"
	end
end

-- Check if process with given PID is running
local function is_process_running(pid)
	return vim.uv.kill(pid, 0) == 0
end

-- Read daemon PID from file and check if it's running
---@param pid_path string
---@return integer|nil pid, boolean running
local function read_daemon_pid(pid_path)
	if vim.fn.filereadable(pid_path) == 0 then
		return nil, false
	end
	local pid_content = vim.fn.readfile(pid_path)
	if #pid_content == 0 then
		return nil, false
	end
	local pid = tonumber(pid_content[1])
	if not pid then
		return nil, false
	end
	return pid, is_process_running(pid)
end

local function get_binary_path()
	local plugin_dir = vim.fn.fnamemodify(debug.getinfo(1, "S").source:sub(2), ":h:h:h")
	local binary_name = "cursortab"
	if is_windows then
		binary_name = binary_name .. ".exe"
	end
	return plugin_dir .. "/server/" .. binary_name
end

-- Clean up stale socket, pid, and config files
local function cleanup_stale_files()
	local state_dir = config.get().state_dir
	local paths = {
		get_ipc_path(state_dir),
		state_dir .. "/cursortab.pid",
		state_dir .. "/cursortab.config.json",
	}
	for _, path in ipairs(paths) do
		if vim.fn.filereadable(path) == 1 then
			vim.fn.delete(path)
		end
	end
end

local function build_config_json()
	local cfg = config.get()
	local v = vim.version()
	local api_key = config.resolve_api_key()
	local endpoint = {
		url = cfg.url,
		api_key = api_key,
		model = cfg.model,
	}
	if cfg.max_tokens and cfg.max_tokens.type then
		endpoint.max_tokens = cfg.max_tokens.type
	end
	local provider = { endpoint = endpoint }
	if type(cfg.next_edit) == "table" then
		local next_edit = {
			url = cfg.url,
			api_key = api_key,
			model = cfg.next_edit.model or "auto",
		}
		if cfg.max_tokens and cfg.max_tokens.edit then
			next_edit.max_tokens = cfg.max_tokens.edit
		end
		provider.next_edit = next_edit
	end
	if cfg.context_size then
		provider.context_size = cfg.context_size
	end
	if cfg.fim_tokens then
		provider.fim_tokens = cfg.fim_tokens
	end
	provider.retrieval_enabled = cfg.retrieval.enabled
	provider.retrieval_max_chunks = cfg.retrieval.max_chunks
	provider.logprobs = cfg.quality.logprobs
	provider.min_confidence = cfg.quality.min_confidence

	-- UI config is Lua-only (highlights), not sent to the daemon
	return vim.json.encode({
		ns_id = ns_id,
		log_level = cfg.log_level,
		state_dir = cfg.state_dir,
		behavior = {
			idle_completion_delay = cfg.behavior.idle_completion_delay,
			text_change_debounce = cfg.behavior.text_change_debounce,
			max_visible_lines = cfg.behavior.max_visible_lines,
			disabled_in = cfg.behavior.disabled_in,
			complete_in_insert = vim.tbl_contains(cfg.behavior.enabled_modes, "insert"),
			complete_in_normal = vim.tbl_contains(cfg.behavior.enabled_modes, "normal"),
			cursor_prediction = {
				enabled = cfg.behavior.cursor_prediction.enabled,
				auto_advance = cfg.behavior.cursor_prediction.auto_advance,
				proximity_threshold = cfg.behavior.cursor_prediction.proximity_threshold,
			},
		},
		provider = provider,
		debug = {
			immediate_shutdown = cfg.debug.immediate_shutdown,
		},
	})
end

local function build_env()
	local env = vim.fn.environ()
	env.CURSORTAB_CONFIG = last_config_json or build_config_json()
	return env
end

-- Establish the RPC relay job, its on_exit feeds the reconnect loop so a dead relay never stays silent
---@return boolean
local function rpc_connect()
	local old = chan
	-- Clear chan first so a deliberate replacement cannot schedule a duplicate reconnect
	chan = nil
	if old and old > 0 then
		pcall(vim.fn.jobstop, old)
	end

	local binary_path = get_binary_path()
	local env = last_env or build_env()
	local job_id = vim.fn.jobstart({ binary_path }, {
		rpc = true,
		env = env,
		on_exit = function(exited_id)
			if chan == exited_id then
				chan = nil
				if not stopping and is_enabled then
					reconnect.last_error = "rpc relay exited"
					if schedule_reconnect then
						schedule_reconnect()
					end
				end
			end
		end,
	})
	if not job_id or job_id <= 0 then
		reconnect.last_error = "failed to start rpc relay"
		if schedule_reconnect then
			schedule_reconnect()
		end
		return false
	end
	chan = job_id
	return true
end

-- Poll for the daemon socket, then connect the relay.
---@param attempts integer
local function connect_when_ready(attempts)
	local ipc_path = get_ipc_path(config.get().state_dir)
	if vim.fn.filereadable(ipc_path) == 1 then
		if not rpc_connect() then
			-- rpc_connect already scheduled the next reconnect attempt
			return
		end
		if reconnect.attempt > 0 then
			vim.notify("cursortab: reconnected to the daemon", vim.log.levels.INFO)
		end
		reconnect.attempt = 0
		reconnect.waiting = false
		reconnect.degraded = false
		reconnect.last_error = ""
		if daemon.on_connected then
			daemon.on_connected()
		end
		return
	end
	if attempts <= 0 then
		reconnect.degraded = true
		vim.notify(
			"cursortab: daemon failed to create IPC file at " .. ipc_path .. " (timed out)",
			vim.log.levels.ERROR
		)
		return
	end
	vim.defer_fn(function()
		if stopping or not is_enabled then
			return
		end
		connect_when_ready(attempts - 1)
	end, 100)
end

-- Launch the daemon process if it is not already running.
---@return boolean false when the binary is missing (fatal)
local function ensure_daemon_process()
	local cfg = config.get()
	vim.fn.mkdir(cfg.state_dir, "p")
	local binary_path = get_binary_path()
	if vim.fn.executable(binary_path) == 0 then
		reconnect.degraded = true
		vim.notify(
			"cursortab binary not found at: "
				.. binary_path
				.. "\n"
				.. "Please ensure the Go server was built during installation.\n"
				.. "If using lazy.nvim, make sure the build step is configured:\n"
				.. 'build = "cd server && go build"',
			vim.log.levels.ERROR
		)
		return false
	end

	local pid_path = cfg.state_dir .. "/cursortab.pid"
	local _, running = read_daemon_pid(pid_path)
	if running then
		return true
	end

	local ipc_path = get_ipc_path(cfg.state_dir)
	if vim.fn.filereadable(ipc_path) == 1 then
		vim.fn.delete(ipc_path)
	end
	if vim.fn.filereadable(pid_path) == 1 then
		vim.fn.delete(pid_path)
	end

	last_config_json = last_config_json or build_config_json()
	last_env = last_env or build_env()
	vim.fn.jobstart({ binary_path, "--daemon" }, { env = last_env, detach = true })
	local config_path = cfg.state_dir .. "/cursortab.config.json"
	vim.fn.writefile({ last_config_json }, config_path)
	return true
end

schedule_reconnect = function()
	if stopping or not is_enabled or reconnect.waiting or reconnect.degraded then
		return
	end
	reconnect.attempt = reconnect.attempt + 1
	if reconnect.attempt > MAX_RECONNECT_ATTEMPTS then
		reconnect.degraded = true
		vim.notify(
			string.format(
				"cursortab: lost connection to the daemon (%s), giving up after %d attempts. Run :CursortabRestart to retry.",
				reconnect.last_error,
				MAX_RECONNECT_ATTEMPTS
			),
			vim.log.levels.ERROR
		)
		return
	end
	reconnect.waiting = true
	local delay = math.min(100 * 2 ^ (reconnect.attempt - 1), 2000)
	vim.defer_fn(function()
		reconnect.waiting = false
		if stopping or not is_enabled or reconnect.degraded then
			return
		end
		if ensure_daemon_process() then
			connect_when_ready(20)
		end
	end, delay)
end

local function start_daemon()
	reconnect = { attempt = 0, waiting = false, degraded = false, last_error = "" }
	stopping = false

	local cfg = config.get()
	local state_dir = cfg.state_dir
	vim.fn.mkdir(state_dir, "p")

	local binary_path = get_binary_path()
	if vim.fn.executable(binary_path) == 0 then
		vim.notify(
			"cursortab binary not found at: "
				.. binary_path
				.. "\n"
				.. "Please ensure the Go server was built during installation.\n"
				.. "If using lazy.nvim, make sure the build step is configured:\n"
				.. 'build = "cd server && go build"',
			vim.log.levels.ERROR
		)
		return false
	end

	local json_config = build_config_json()
	local env = vim.fn.environ()
	env.CURSORTAB_CONFIG = json_config
	last_env = env
	last_config_json = json_config

	local ipc_path = get_ipc_path(state_dir)
	local pid_path = state_dir .. "/cursortab.pid"
	local config_path = state_dir .. "/cursortab.config.json"

	local function finish_start(needs_process)
		if needs_process then
			-- Defer process creation so UI renders first (Windows CreateProcess blocks ~1.5s)
			vim.defer_fn(function()
				if stopping or not is_enabled then
					return
				end
				if ensure_daemon_process() then
					connect_when_ready(100)
				end
			end, 0)
			return
		end
		rpc_connect()
	end

	local needs_process = false
	if vim.fn.filereadable(ipc_path) == 0 then
		needs_process = true
	else
		local _, running = read_daemon_pid(pid_path)
		if not running then
			needs_process = true
		elseif vim.fn.filereadable(config_path) == 1 then
			local stored = table.concat(vim.fn.readfile(config_path), "\n")
			if stored ~= json_config then
				daemon.stop_daemon(function()
					finish_start(true)
				end)
				return true
			end
		end
	end

	finish_start(needs_process)
	return true
end

-- Public API

---Build the event payload for a buffer (§4 keys: tick, path, row, col, top,
---bot, leftcol, mode plus any extra keys such as changed/full).
---@param bufnr integer|nil Buffer the event refers to
---@param extra table|nil Extra payload keys (changed, full)
---@return table|nil payload
function daemon.build_payload(bufnr, extra)
	if not bufnr or not vim.api.nvim_buf_is_valid(bufnr) then
		return nil
	end

	local row, col = 1, 0
	local top, bot, leftcol, width = 0, 0, 0, 0
	local win = nil
	for _, candidate in ipairs(vim.api.nvim_list_wins()) do
		if vim.api.nvim_win_is_valid(candidate) and vim.api.nvim_win_get_buf(candidate) == bufnr then
			win = candidate
			break
		end
	end
	if win then
		local cursor = vim.api.nvim_win_get_cursor(win)
		row, col = cursor[1], cursor[2]
		local info = vim.fn.getwininfo(win)[1]
		if info then
			top = info.topline
			bot = info.botline
			leftcol = info.leftcol
			width = math.max(0, vim.api.nvim_win_get_width(win) - (info.textoff or 0))
		end
	end

	local mode = vim.api.nvim_get_mode().mode:sub(1, 1)
	local payload = {
		tick = vim.api.nvim_buf_get_changedtick(bufnr),
		path = vim.api.nvim_buf_get_name(bufnr),
		row = row,
		col = col,
		top = top,
		bot = bot,
		leftcol = leftcol,
		width = width,
		mode = mode == "i" and "i" or "n",
	}
	if extra then
		for key, value in pairs(extra) do
			payload[key] = value
		end
	end
	return payload
end

---Send a cursortab_event notification. A send failure schedules an async
---reconnect with backoff instead of dead-ending the channel.
---@param name string Event name
---@param payload table|nil Payload from build_payload
---@return boolean sent
function daemon.send_payload(name, payload)
	if not payload or not chan or chan <= 0 then
		return false
	end
	local ok, err = pcall(vim.fn.rpcnotify, chan, "cursortab_event", name, payload)
	if not ok then
		reconnect.last_error = tostring(err)
		if schedule_reconnect then
			schedule_reconnect()
		end
		return false
	end
	return true
end

-- Get the namespace ID
function daemon.get_namespace_id()
	return ns_id
end

-- Enable/disable daemon functionality
---@param enabled boolean
function daemon.set_enabled(enabled)
	is_enabled = enabled
end

function daemon.is_enabled()
	return is_enabled
end

---Whether the RPC relay channel is live
---@return boolean
function daemon.is_connected()
	return chan ~= nil and chan > 0
end

-- Called after every successful (re)connect. events.lua wires this to a full resync
---@type fun()|nil
daemon.on_connected = nil

-- Check daemon process status
function daemon.check_daemon_status()
	local cfg = config.get()
	local state_dir = cfg.state_dir
	local ipc_path = get_ipc_path(state_dir)
	local pid_path = state_dir .. "/cursortab.pid"

	local status = {
		socket_exists = vim.fn.filereadable(ipc_path) == 1,
		pid_file_exists = vim.fn.filereadable(pid_path) == 1,
		daemon_running = false,
		pid = nil,
	}

	local pid, running = read_daemon_pid(pid_path)
	if pid then
		status.pid = pid
		status.daemon_running = running
	end

	return status
end

-- Get connection and reconnect status for checkhealth
function daemon.get_connection_status()
	return {
		connected = chan ~= nil and chan > 0,
		channel_id = chan,
		reconnect_attempts = reconnect.attempt,
		reconnecting = reconnect.waiting,
		degraded = reconnect.degraded,
		last_error = reconnect.last_error,
	}
end

-- Get the installed Go binary version, if available
function daemon.get_binary_version()
	local binary_path = get_binary_path()
	if vim.fn.executable(binary_path) == 0 then
		return nil
	end

	local output = vim.fn.system({ binary_path, "--version" })
	if vim.v.shell_error ~= 0 then
		return nil
	end

	return vim.trim(output)
end

-- Stop the daemon without blocking the UI, on_done runs after cleanup or the forced kill fallback, driven by defer timers only
---@type fun()|nil
function daemon.stop_daemon(on_done)
	stopping = true
	if chan and chan > 0 then
		pcall(vim.fn.jobstop, chan)
	end
	chan = nil
	reconnect.waiting = false

	local state_dir = config.get().state_dir
	local pid_path = state_dir .. "/cursortab.pid"

	local function finish()
		stopping = false
		if on_done then
			on_done()
		end
	end

	local pid, running = read_daemon_pid(pid_path)
	if not running then
		cleanup_stale_files()
		finish()
		return
	end

	-- libuv maps sigterm to TerminateProcess on Windows
	pcall(vim.uv.kill, pid, "sigterm")

	local tries = 0
	local function poll()
		if not is_process_running(pid) then
			cleanup_stale_files()
			finish()
			return
		end
		tries = tries + 1
		if tries >= 30 then
			pcall(vim.uv.kill, pid, "sigkill")
			cleanup_stale_files()
			finish()
			return
		end
		vim.defer_fn(poll, 100)
	end
	vim.defer_fn(poll, 100)
end

-- Force start daemon (for use after stop_daemon)
function daemon.force_start()
	return start_daemon()
end

return daemon
