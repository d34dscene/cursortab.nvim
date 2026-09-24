-- End-to-end smoke: real daemon + real server, asserts a completion renders.
-- Usage:
--   CURSORTAB_E2E_URL=https://your-server nvim --headless -u NONE \
--     -c 'luafile scripts/e2e-smoke.lua'
-- Exits 0 on E2E_COMPLETION_SHOWN, 1 otherwise. Point it at any
-- OpenAI-compatible /v1/completions server (llama.cpp, router, ...).

vim.o.swapfile = false
vim.opt.runtimepath:append(vim.fn.getcwd())

local function fail(err)
	print("E2E_ERROR " .. tostring(err))
	vim.cmd("silent! qa!")
	os.exit(1)
end

local ok, run_err = xpcall(function()
	local url = vim.env.CURSORTAB_E2E_URL or "http://localhost:8000"
	local state = vim.fn.tempname()

	require("cursortab").setup({
		url = url,
		model = "auto",
		next_edit = { model = "auto" },
		state_dir = state,
		log_level = "debug",
	})

	vim.cmd("edit! " .. state .. "-main.go")
	vim.bo.filetype = "go"
	vim.api.nvim_buf_set_lines(0, 0, -1, false, {
		"package main",
		"",
		"func add(a, b int) int {",
		"\treturn ",
	})

	local daemon = require("cursortab.daemon")
	local connected = vim.wait(20000, function()
		local st = daemon.get_connection_status()
		return st and st.connected
	end, 100)
	print("daemon_connected", connected)

	vim.api.nvim_win_set_cursor(0, { 4, 8 })
	vim.api.nvim_buf_set_lines(0, 3, 4, false, { "\treturn a" })

	local ui = require("cursortab.ui")
	local shown = vim.wait(15000, function()
		return ui.has_completion() or ui.has_cursor_prediction()
	end, 100)
	print("e2e_result", shown and "E2E_COMPLETION_SHOWN" or "E2E_TIMEOUT_NO_COMPLETION")
	print("daemon_log", state .. "/cursortab.log")
	vim.cmd("silent! qa!")
	os.exit(shown and 0 or 1)
end, debug.traceback)

if not ok then
	fail(run_err)
end
