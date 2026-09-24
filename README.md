# cursortab.nvim

Ghost-text completion and cursor jumps for Neovim, backed by any llama.cpp
server. Dual mode runs a FIM model while you type (mellum, qwen) and an edit
model on pause (zeta, sweep) for next-edit rewrites and Tab-to-jump cursor
targets. Models, prompt format and context window are auto-detected on
connect.

<p align="center">
    <img src="assets/demo.gif" width="600">
</p>

<!-- mtoc-start -->

- [Requirements](#requirements)
- [Installation](#installation)
  - [Using lazy.nvim](#using-lazynvim)
  - [Using packer.nvim](#using-packernvim)
- [Configuration](#configuration)
- [Dual mode](#dual-mode)
- [llama.cpp serving](#llamacpp-serving)
  - [Model guidance](#model-guidance)
- [Commands](#commands)
- [Keymaps](#keymaps)
- [Highlight groups](#highlight-groups)
- [Advanced configuration](#advanced-configuration)
- [FAQ](#faq)
- [Eval harness](#eval-harness)
- [Benchmarks](#benchmarks)
- [Contributing](#contributing)
- [License](#license)

<!-- mtoc-end -->

## Requirements

- Neovim 0.8+
- Go 1.25+ to build the daemon
- A llama.cpp server, local or remote

## Installation

### Using [lazy.nvim](https://github.com/folke/lazy.nvim)

```lua
{
  "cursortab/cursortab.nvim",
  lazy = false,  -- the daemon starts with Neovim
  build = "cd server && go build",
  config = function()
    require("cursortab").setup({
      url = "http://localhost:8000",
      model = "auto",
      next_edit = { model = "auto" },
    })
  end,
}
```

### Using [packer.nvim](https://github.com/wbthomason/packer.nvim)

```lua
use {
  "cursortab/cursortab.nvim",
  run = "cd server && go build",
  config = function()
    require("cursortab").setup({
      url = "http://localhost:8000",
      model = "auto",
      next_edit = { model = "auto" },
    })
  end,
}
```

## Configuration

The whole required config is three lines:

```lua
require("cursortab").setup({
  url = "http://localhost:8000",  -- or https://your-server
  model = "auto",                 -- type model, auto picks mellum > qwen > plain
  next_edit = { model = "auto" }, -- edit model, auto picks zeta-2.1 > zeta-2 > sweep
})
```

Drop `next_edit` to run single mode. An idle buffer then asks the type model
instead of the edit model.

Everything else is probed when the daemon connects:

- The model list comes from `GET /v1/models`. It feeds `model = "auto"` and
  the `:Cursortab model` picker.
- The prompt format and FIM tokens come from the model family. Mellum gets
  its `<fim_prefix>` style tokens, Qwen gets repo tokens, and
  `POST /tokenize` probes the vocabulary for unknown models.
- The context window comes from `GET /props` and falls back to 8192.
- Sampling stays server side. Every request sends `cache_prompt = true` so
  llama.cpp reuses the cached prefix.

Run `:help cursortab-config` for the full option reference.

## Dual mode

One ghost text surface, two roles.

- Keystrokes debounce for 35ms, then the type model answers with a FIM
  completion.
- A 400ms pause hands the buffer to the edit model. A real rewrite replaces
  the ghost text. The answer can carry a cursor target, and Tab jumps to it.
- An empty edit answer means nothing to do. The valid type ghost already on
  screen stays.
- Edit errors and timeouts are logged at Info level and never disable the
  type path.
- Any keystroke supersedes in-flight requests and re-triggers the type model.
- Tab, S-Tab and Esc act on whatever is displayed, whichever role produced
  it.

With `model = "auto"` the type role prefers mellum, then qwen, then plain
FIM. With `next_edit.model = "auto"` the edit role prefers zeta-2.1, then
zeta-2, then sweep.

## llama.cpp serving

Serving flags matter more for perceived speed than the model choice:

- `--cache-reuse` reuses the KV cache for the unchanged prompt prefix
  across keystrokes. This is what makes the request after a keystroke fast.
- Speculative decoding with `--model-draft` (a small model of the same
  family) typically doubles or triples decode throughput.
- Keep generations short. The type role defaults to 64 tokens and the edit
  role to 256.

```bash
llama-server -hf unsloth/Qwen3.5-0.8B-GGUF:Q8_0 --port 8000 --cache-reuse
```

One router can host both roles and autoload each model on first use. A
router that sleeps models adds cold wake latency to the first request after
idle.

### Model guidance

| Role              | Recommended                              | Notes                                                                    |
| ----------------- | ---------------------------------------- | ------------------------------------------------------------------------ |
| Type, while typing | `mellum-4b`, `qwen3.5-0.8B` to `4B`     | FIM, fast, 64 token budget                                               |
| Edit, on pause    | `zeta-2.1`, `zeta-2`, `sweep-next-edit`  | Multi-edit rewrites and cursor jumps, quiet when nothing to do           |

## Commands

- `:Cursortab model`: probe the server, pick models from a grouped picker,
  then restart the daemon
- `:Cursortab toggle`: enable or disable the plugin
- `:Cursortab restart`: restart the daemon
- `:Cursortab status`: show daemon and connection status
- `:Cursortab log`: open the daemon log
- `:Cursortab clear_log`: clear the daemon log
- `:checkhealth cursortab`: probe results, configured models, connection and
  reconnect state

## Keymaps

- Tab accepts the shown completion, or jumps to the cursor target
- S-Tab partially accepts, one word for short completions and one line for
  multi-line ones
- Esc hides the completion
- The trigger keymap is off by default. Bind it to request a completion by
  hand. Manual triggers bypass every suppression gate.

Jump indicators appear when an edit model changes a distant region. The
label reads TAB and shows the line distance when the target is off screen.

## Highlight groups

The plugin defines the following highlight groups with `default = true`, so you
can override them in your colorscheme or config:

| Group                   | Default                            | Purpose                        |
| ----------------------- | ---------------------------------- | ------------------------------ |
| `CursorTabDeletion`     | `bg = "#4f2f2f"`                   | Background for deleted text    |
| `CursorTabAddition`     | `bg = "#394f2f"`                   | Background for added text      |
| `CursorTabModification` | `bg = "#282e38"`                   | Background for modified text   |
| `CursorTabCompletion`   | `fg = "#80899c"`                   | Foreground for completion text |
| `CursorTabJumpSymbol`   | `fg = "#373b45"`                   | Jump indicator symbol          |
| `CursorTabJumpText`     | `bg = "#373b45"`, `fg = "#bac1d1"` | Jump indicator text            |

To customize, set the highlight before or after calling `setup()`:

```lua
vim.api.nvim_set_hl(0, "CursorTabAddition", { bg = "#1a3a1a" })
```

## Advanced configuration

<details>
<summary>Full config</summary>

```lua
require("cursortab").setup({
  url = "http://localhost:8000",
  model = "auto",                 -- type model, "auto" probes /v1/models
  next_edit = { model = "auto" }, -- edit model, remove to disable dual mode

  api_key_env = "",   -- env var read at daemon startup, sent as the api key
  max_tokens = nil,   -- generation caps, defaults { type = 64, edit = 256 }
  context_size = nil, -- nil probes /props, falls back to 8192
  fim_tokens = nil,   -- explicit FIM tokens for exotic models, nil is probed
  quality = { logprobs = false, min_confidence = 0 },
  retrieval = { enabled = false, max_chunks = 0 },
  log_level = "info", -- trace, debug, info, warn, error
  state_dir = nil,    -- nil means stdpath("state")/cursortab

  keymaps = {
    accept = "<Tab>",           -- accept the shown completion
    partial_accept = "<S-Tab>", -- accept the next word or line
    trigger = false,            -- manual trigger keymap, off by default
  },

  ui = {
    completions = {
      addition_style = "dimmed", -- "dimmed" or "highlight"
      fg_opacity = 0.6,          -- 0 invisible, 1 fully visible
    },
    jump = {
      symbol = "",
      text = " TAB ",
      show_distance = true,      -- line distance for off-screen jumps
    },
  },

  behavior = {
    idle_completion_delay = 400, -- pause before the edit model runs
    text_change_debounce = 35,   -- ms after typing before the type model runs
    max_visible_lines = 12,      -- 0 disables
    disabled_in = {},            -- treesitter scopes, e.g. { "comment", "string" }
    enabled_modes = { "insert", "normal" },
    cursor_prediction = {
      enabled = true,
      auto_advance = true,
      proximity_threshold = 3,   -- 0 disables
    },
    ignore_paths = {             -- glob patterns that skip completions
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

  debug = { immediate_shutdown = false },
})
```

- `fim_tokens` takes `prefix`, `suffix`, `middle`, `repo_name`, `file_sep`,
  `filename` and `suffix_first`. Set it only when detection guesses wrong.
- `quality.min_confidence` floors on mean token logprob, so a gating value
  is negative such as -1.5. 0 disables the gate.
- `retrieval.max_chunks` is 0 for the default of 8 chunks.
- `state_dir` holds the daemon log at `state_dir/cursortab.log`.

</details>

## FAQ

<details>
<summary>Completions are not showing</summary>

Run `:checkhealth cursortab` first. It reports the probe results, the
configured models and the connection state.

Then open the log with `:Cursortab log`. Every suppression is logged at
Info level with one of these reasons:

- `no-edits`: an idle or edit request found nothing to rewrite
- `disabled-scope`: the cursor sits in a treesitter scope from
  `behavior.disabled_in`
- `single-deletion`: the change would only delete text
- `low-confidence`: mean logprob is below `quality.min_confidence`
- `rejection-cache`: a similar completion was rejected recently
- `stale`: a newer keystroke superseded the request
- `mode`: the current mode is not in `behavior.enabled_modes`

A manual trigger bypasses every gate.

</details>

<details>
<summary>Completions are slow</summary>

- A router that sleeps models reloads on the first request after idle. This
  is cold model wake. Later requests are fast again.
- Check `--cache-reuse`, it reuses the KV cache across keystrokes.
- Add speculative decoding with `--model-draft`.
- Lower `max_tokens` and quantize harder.
- Lower `context_size` to send less input.

</details>

<details>
<summary>Remote server or timeouts</summary>

- `:checkhealth cursortab` shows whether the connection is up and what the
  probe resolved.
- Set `url` to the full server origin, including scheme and port.
- Set `api_key_env` for authenticated servers. The daemon reads the
  environment at startup, so run `:Cursortab restart` after changing it.

</details>

<details>
<summary>Switching models</summary>

Run `:Cursortab model`. It probes `GET /v1/models`, opens a picker grouped
by role and restarts the daemon with your choices.

</details>

<details>
<summary>Updating the plugin</summary>

Pull with your plugin manager, then restart the daemon with
`:Cursortab restart`.

</details>

## Eval harness

The eval harness records editing scenarios and replays them against local
targets.

- `CURSORTAB_EVAL_URL`: server to record against, defaults to
  http://localhost:8000
- `CURSORTAB_EVAL_API_KEY`: optional, local servers need none
- `just eval-record <target>`: record cassettes for one target
- `just eval`: run the suite and fill baseline.json
- `just eval-check`: verify results against baseline.json, which ratchets

Cassettes live in sidecar directories next to their scenarios.

## Benchmarks

50 scenario instances, 25 quality and 25 suppress, recorded against a
llama.cpp router. These are first-party numbers on a new scenario set. They
are not comparable to the table in older README versions.

- **Score**: deltaChrF x F1(show, quiet) with deltaChrF as a fraction.
  F1 is the harmonic mean of show rate and quiet rate.
- **deltaChrF**: edit quality when shown, a character n-gram F-score on the
  diff region.
- **Show rate**: fraction of quality scenarios where a completion was shown.
- **Quiet rate**: fraction of suppress scenarios where nothing was shown.

| Target                  | Dialect       | Score | deltaChrF | Show rate | Quiet rate | p50 (ms) | p90 (ms) |
| ----------------------- | ------------- | ----: | --------: | --------: | ---------: | -------: | -------: |
| zeta-2.1                | edit-zeta21   |  0.26 |     34.7 |       60% |       100% |      921 |     1543 |
| zeta-2                  | edit-zeta2    |  0.23 |     32.6 |       56% |        96% |      843 |     1613 |
| qwen3.5-0.8B            | fim-qwen      |  0.20 |     34.2 |       84% |        44% |      120 |      410 |
| mellum-4b               | fim-mellum    |  0.16 |     22.9 |       52% |       100% |       55 |      824 |
| sweep-next-edit-v2-7B   | edit-sweep    |  0.11 |     25.8 |       48% |        40% |      354 |      673 |

## Contributing

Contributions are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for build,
test, and eval instructions.

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE)
file for details.
