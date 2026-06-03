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
- **Incremental Knowledge Base**: The knowledge base is built once and updated incrementally. Subsequent runs of the `generate` command only fetch and index new PRs.
- **Per-Repo Configuration**: Data and settings are stored within each repository's `.git` directory, ensuring projects remain isolated and don't conflict.
- **Configurable LLM Backends**: Seamlessly switch between providers:
    - **GitHub/Copilot**: GPT, Claude, ...
    - **Google Gemini**: Flash, Pro, ...
    - **Ollama**: Run reviews 100% locally with Llama 3 or other models.

## Installation

### 1. Global Installation (Recommended)

#### Option A: Snap (Linux)
Install the `ragrev` snap from the store:

```bash
sudo snap install ragrev
```

After installation, you **must** connect the interface to allow access to the local `.git` directory:

```bash
sudo snap connect ragrev:dot-git-access
```

Connect optional interfaces with:
```bash
sudo snap connect ragrev:<interface-name>
```

##### Snap Interface Connections

When using the Snap version, certain permissions are required based on your usage:

| Interface | Status | Purpose |
| :--- | :--- | :--- |
| `dot-git-access` | **Manual** | **Required**. Allows managing the knowledge base in `.git/ragrev`. |
| `dot-config-gh` | Manual | Optional. Allows `ragrev` to reuse your system's `gh` CLI login. |
| `password-manager-service` | Manual | Optional. Required if your `gh` token is stored in the system keyring. |
| `removable-media` | Manual | Optional. Required if your project is on an external drive. |

#### Option B: Go Install
```bash
go install github.com/canonical/ragrev/cmd/ragrev@latest
```


### 2. Build the Knowledge Base

Run this inside your git repository to index past PRs and their comments. This step only needs to be run once initially; future runs will only fetch and append new comments incrementally.

```bash
ragrev generate
```

### 3. Review a Pull Request

To perform an AI review on a specific PR:

```bash
ragrev review 123
```

To filter the knowledge base for comments from specific reviewers (Persona Filtering):

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

Settings are stored in `.git/ragrev/config.yml` and are specific to the current repository.

## Requirements

- A GitHub repository with a configured `origin` remote.
- A GitHub token (for API access).
- (Optional) API keys for other LLM backends if chosen.
