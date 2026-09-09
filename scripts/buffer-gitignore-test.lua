-- Regression test: the async git check-ignore result must invalidate the
-- buffer state cache without erroring when the spawned process exits.
local source = debug.getinfo(1, "S").source:sub(2)
local plugin_dir = vim.fn.fnamemodify(source, ":p:h:h")
vim.opt.rtp:prepend(plugin_dir)

local function assert_equal(expected, actual, label)
	if expected ~= actual then
		error(string.format("%s: expected %s, got %s", label, vim.inspect(expected), vim.inspect(actual)))
	end
end

local original_cwd = vim.fn.getcwd()
local repo_dir = vim.fn.tempname()

local function run()
	vim.fn.mkdir(repo_dir, "p")
	vim.fn.writefile({ "ignored.txt" }, repo_dir .. "/.gitignore")
	vim.fn.writefile({ "x" }, repo_dir .. "/ignored.txt")
	vim.fn.chdir(repo_dir)
	assert_equal(1, vim.fn.filereadable("ignored.txt"), "cwd is the temp git repo")
	vim.fn.system({ "git", "init", "-q" })
	assert_equal(0, vim.v.shell_error, "git init")

	local config = require("cursortab.config")
	config.setup({
		enabled = false,
		behavior = {
			ignore_filetypes = {},
			ignore_paths = {},
			ignore_gitignored = true,
		},
	})

	local buffer = require("cursortab.buffer")
	vim.cmd("edit " .. vim.fn.fnameescape(repo_dir .. "/ignored.txt"))

	-- The gitignore check starts optimistic before the async git result lands.
	assert_equal(false, buffer.should_skip(), "should_skip before the git check finishes")

	local flipped = vim.wait(5000, function()
		return buffer.should_skip()
	end, 50)
	assert_equal(true, flipped, "should_skip after the async git check lands")
end

local ok, err = pcall(run)
vim.fn.chdir(original_cwd)
vim.fn.delete(repo_dir, "rf")
if not ok then
	error(err)
end
print("buffer-gitignore-test: PASS")
vim.cmd("qa!")
