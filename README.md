# ragrev

`ragrev` is an AI-powered code review tool that uses **Local Retrieval-Augmented Generation (RAG)** to provide high-quality, context-aware feedback on GitHub Pull Requests. By learning from your project's historical review comments, `ragrev` ensures its suggestions are consistent with your team's existing coding standards and style.

## Value

Traditional AI code reviewers often provide generic advice. `ragrev` changes this by:
- **Grounding in Context**: It retrieves relevant past review discussions to ground its suggestions in the reality of your codebase.
- **Consistency**: Helps maintain team-specific patterns by surfacing how similar issues were addressed before.
- **Reduced Noise**: By understanding what has been accepted or rejected in the past, it provides more meaningful feedback.
- **Privacy & Performance**: Uses a local vector database and local indexing, meaning your codebase's historical data stays on your machine.

## Features

- **AST-Aware Parsing**: Supports Go, Python, and Rust. It uses Abstract Syntax Trees to understand code structure, ensuring comments are placed accurately on relevant functions or types.
- **Persona Filtering**: Review using the "voice" of specific team members. You can filter historical context during the review to only include comments from specific reviewers.
- **Incremental Review Index**: The review index is built once and updated incrementally. Subsequent runs of the `generate` command only fetch and index new PRs.
- **Per-Repo Configuration**: Data and settings are stored within each repository's `.git` directory, ensuring projects remain isolated and don't conflict.
- **Global Configuration**: A per-user global config seeds newly created per-repo configs, so your preferred backend, theme, and other preferences carry over automatically to every new project.
- **Rich Syntax-Highlighted Output**: Review results are rendered with syntax highlighting. The colour theme is configurable via `output_theme` in the config file (any [Chroma theme](https://xyproto.github.io/splash/docs/all.html) is accepted; default: `github`).
- **Token Usage Summary**: After each review, a concise token-cost breakdown is printed — model name, number of LLM calls, retries, skipped chunks, and prompt/output/total token counts with thousands separators.
- **Custom Review Prompt**: Override the built-in system prompt with your own via the `review_prompt` config key, giving full control over the LLM's review instructions.
- **Configurable LLM Backends**: Seamlessly switch between providers:
    - **GitHub/Copilot**: GPT, Claude, and any model available through GitHub Models or Copilot.
    - **Google Gemini**: Flash, Pro, and other Gemini variants.
    - **OpenRouter**: Access hundreds of models — Claude, Llama, GLM, Mistral, and more — through a single API key (`OPENROUTER_API_KEY`). Just set `backend: openrouter` and pick any [OpenRouter model slug](https://openrouter.ai/models) as the `model`.
    - **Ollama**: Run reviews 100% locally with Llama 3 or other models.

## Installation

### 1. Global Installation (Recommended)

#### Option A: Snap (Linux)
Install the `ragrev` snap from the store:

```bash
sudo snap install ragrev
```

The snap is **strictly confined** and works out of the box for repositories
checked out anywhere under your home directory — the auto-connected `home`
interface already covers nested `.git/ragrev` data. No manual interface
connection is required for the common case.

Authenticate by exporting a token in your environment before running ragrev:

```bash
export GITHUB_TOKEN=ghp_...        # GitHub / Copilot backends
export GEMINI_API_KEY=...          # Gemini backend
export OPENROUTER_API_KEY=...      # OpenRouter backend
```

##### Snap Interface Connections

| Interface | Status | Purpose |
| :--- | :--- | :--- |
| `home` | **Auto** | Access repositories (and their `.git/ragrev` index) under your home directory. |
| `network` | **Auto** | Reach the GitHub and LLM APIs. |
| `removable-media` | Manual | Required only if your project lives on an external drive (`/media`, `/mnt`). Connect with `sudo snap connect ragrev:removable-media`. |

> Note: repositories whose `.git` sits directly at your home root, or that live
> outside your home directory and not on removable media, are not accessible
> under confinement by design. Move the repo under `~/` or onto removable media.


#### Option B: Go Install
```bash
go install github.com/canonical/ragrev/cmd/ragrev@latest
```


### 2. Build the Knowledge Base

Run this inside your git repository to index past PRs and their comments. This step only needs to be run once initially; future runs will only fetch and append new comments incrementally.

```bash
ragrev init
```

> If the repository has no PRs or qualifying review comments, the index is
> created empty — `ragrev review` will still work, falling back to a generic
> review without historical context.

### 3. Review a Pull Request

To perform an AI review on a specific PR:

```bash
ragrev review 123
```

To filter the review index for comments from specific reviewers (Persona Filtering):

```bash
ragrev review 123 --reviewers "username1,username2"
```

To submit the findings directly to GitHub as a pending review:

```bash
ragrev review 123 --submit
```

To refresh the vector DB with new PR comments before running the review (defaults to off), use `--update-index`. This runs the `init` indexing pipeline first, incrementally appending any new comments, then reviews against the up-to-date index:

```bash
ragrev review 123 --update-index
```

### 4. Configuration

#### Per-repo config

Customize your backend, repo settings, and default reviewers for the current repository:

```bash
ragrev config
```

Settings are stored in `.git/ragrev/config.yml` and are specific to the current repository. They take precedence over the global config for that repo.

#### Global config

A per-user global config applies default values to **every** repository.  It is also the config shown when you run `ragrev config` outside a git repo.  The fields `repo`, `base_branch`, and `reviewers` are not stored here — they are always per-repo.

```bash
# Open the global config in your editor from anywhere (inside or outside a repo)
ragrev config --global

# Same effect when run outside a git repo
ragrev config
```

**Config file locations**

| Environment | Path |
| :--- | :--- |
| Snap (strict confinement) | `~/snap/ragrev/common/config.yml` — writable without any extra interface connections |
| Standard install | `~/.config/ragrev/config.yml` (respects `$XDG_CONFIG_HOME`) |
| Fallback (no `$XDG_CONFIG_HOME`) | `~/.ragrev/config.yml` |

**Config precedence** (highest wins):

```
CLI flags  >  per-repo .git/ragrev/config.yml  >  global config.yml  >  built-in defaults
```

**Overriding backend settings via flags**

Every field of a backend's config can be overridden at runtime without editing any file.

| Pattern | Effect |
| :--- | :--- |
| `--backend <name>` | Select a backend. |
| `--backend.model <value>` | Override the model for the **selected** backend. |
| `--backend.url <value>` | Override the API endpoint URL for the selected backend. |
| `--backend.token-env <value>` | Override the token environment variable for the selected backend. |
| `--backend.token-cmd <value>` | Override the token command for the selected backend. |
| `--backend.max-prompt-tokens <n>` | Override the max prompt token limit for the selected backend. |
| `--backend.<name>.model <value>` | Override the model for a **specific** backend (also selects it). |
| `--backend.<name>.url <value>` | Override the URL for a specific backend (also selects it). |
| *(same pattern for `token-env`, `token-cmd`, `max-prompt-tokens`)* | |

Examples:

```bash
# Use the gemini backend with a different model
ragrev review 123 --backend.gemini.model gemini-2.5-pro

# Use whichever backend is configured, but swap the model
ragrev review 123 --backend.model claude-opus-4-5

# Point ollama at a non-default URL and select it
ragrev review 123 --backend.ollama.url http://gpu-box:11434/v1/chat/completions
```

**Defining multiple presets of one provider**

Backend keys may use the form `client(alias)` to register several presets that
share the same client implementation but differ in model, URL, or token. The
prefix before `(` selects the client (`github`, `copilot`, `gemini`, `ollama`,
`openrouter`); the alias is free-form. Aliases inherit the base client's `url`
and token settings, so you usually only need to set `model`:

```yaml
backend: github(opus)        # active preset
backends:
  github(opus):
    model: claude-opus-4-5
  github(gemini):
    model: gemini-2.5-pro
```

Select a preset at runtime with `--backend "github(opus)"`, and override its
model on the fly with `--backend.model <value>`.

**Seeding new repos from global**

When a per-repo config is created for the first time (e.g. on the first `ragrev init` or `ragrev review` in a new checkout) its non-repo fields are automatically seeded from the global config — so your preferred `backend`, `output_theme`, `backends`, etc. are already set without any extra action.

**Notable config keys**

| Key | Description |
| :--- | :--- |
| `backend` | Active LLM backend (`copilot`, `gemini`, `openrouter`, `ollama`). |
| `backends.<name>.model` | Model identifier for that backend (e.g. `anthropic/claude-opus-4` for OpenRouter). |
| `output_theme` | Syntax-highlight theme for review output (e.g. `github`, `monokai`, `github-dark`). |
| `review_prompt` | Replaces the built-in system prompt. Leave empty to use the default. |
| `review_scope` | Context preloaded per review: `pr` (default), `file`, or `chunk`. |
| `min_similarity_score` | Minimum vector-similarity (0–1) for a RAG result to be included. |
| `max_examples` | Maximum number of similar past review threads included per chunk. |



## Requirements

- A GitHub repository with a configured `origin` remote.
- A GitHub token via the `GITHUB_TOKEN` environment variable (for API access).
  On non-snap installs, ragrev also falls back to `gh auth token` if the
  GitHub CLI is installed and logged in.
- (Optional) `GEMINI_API_KEY` for the Gemini backend.
- (Optional) `OPENROUTER_API_KEY` for the OpenRouter backend.
