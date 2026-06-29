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

### 4. Configuration

Customize your backend, repo settings, and default reviewers:

```bash
ragrev config
```

Settings are stored in `.git/ragrev/config.yml` and are specific to the current repository. Notable options added recently:

| Key | Description |
| :--- | :--- |
| `backend` | Active LLM backend (`copilot`, `gemini`, `openrouter`, `ollama`). |
| `backends.<name>.model` | Model identifier for that backend (e.g. `anthropic/claude-opus-4` for OpenRouter). |
| `output_theme` | Syntax-highlight theme for review output (e.g. `github`, `monokai`, `github-dark`). |
| `review_prompt` | Replaces the built-in system prompt. Leave empty to use the default. |

## Requirements

- A GitHub repository with a configured `origin` remote.
- A GitHub token via the `GITHUB_TOKEN` environment variable (for API access).
  On non-snap installs, ragrev also falls back to `gh auth token` if the
  GitHub CLI is installed and logged in.
- (Optional) `GEMINI_API_KEY` for the Gemini backend.
- (Optional) `OPENROUTER_API_KEY` for the OpenRouter backend.
