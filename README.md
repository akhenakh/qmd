# qmd (Go Port)

A Go implementation of **Quick Markdown Search**, originally created by [Tobias Lütke](https://github.com/tobi).

> **Original Project:** [github.com/tobi/qmd](https://github.com/tobi/qmd)

`qmd` is a command-line tool for indexing and searching markdown notes.  
It supports both full-text search (using SQLite FTS5) and semantic vector search.  
You can generate embeddings using either an **Ollama** server or **Local Inference** (via `llama.cpp` / `yzma`).

## Features

- **Full-Text Search**: Fast keyword search using BM25 with context extraction.
- **Vector Search**: Semantic search using embeddings (Ollama or Local).
- **Smart Splitting**: Uses context-aware Markdown splitting (via LangChainGo) for better embedding quality.
- **MCP Server**: Exposes search capabilities via the [Model Context Protocol](https://modelcontextprotocol.io), allowing AI assistants (like Claude Desktop) to search your notes.
- **Local First**: All data is stored locally in `~/.cache/qmd/`.
- **Configurable**: Persist your settings in `~/.config/qmd.yml`.

## Prerequisites

1. **Go 1.25+**
2. **C Compiler** (gcc or clang) for SQLite extensions.

### Option A: Ollama (Recommended for simplicity)

Install and run **[Ollama](https://ollama.com/)**.

### Option B: Local Inference (Recommended for performance/control)

If you prefer to run the model directly within `qmd` without an external server, you need the `llama.cpp` shared libraries. We use [yzma](https://github.com/hybridgroup/yzma) to manage these.

1. **Install the `yzma` tool:**
   ```bash
   go install github.com/hybridgroup/yzma/cmd/yzma@latest
   ```

2. **Download the `llama.cpp` libraries:**
   
   Choose a location (e.g., `~/lib/yzma`).

   ```bash
   # CPU Only
   yzma install --lib ~/lib/yzma

   # CUDA (NVIDIA GPU)
   yzma install --lib ~/lib/yzma --processor cuda
   ```

3. **Download an Embedding Model:**
   You will need a GGUF model file (e.g., `nomic-embed-text-v1.5.Q4_K_M.gguf`).

## Installation

```bash
# Clone the repository
git clone https://github.com/akhenakh/qmd.git
cd qmd

# Build with FTS5 support (Required)
go build -tags sqlite_fts5
```

## Configuration

`qmd` stores its configuration in `~/.config/qmd.yml`. This file is created automatically on the first run, but you can create/edit it manually to persist your settings.

**Example `~/.config/qmd.yml`:**

```yaml
ollama_url: http://localhost:11434
model_name: nomic-embed-text
embed_dimensions: 768
chunk_size: 1000
chunk_overlap: 200

# Local Inference Settings
use_local: false
local_model_path: /path/to/nomic-embed-text-v1.5.Q8_0.gguf
local_lib_path: /home/user/qmd/lib
```

## Usage

### 1. Indexing Notes

Add a directory of markdown files to the index:

```bash
qmd add ~/Documents/Notes
```

To update the index later (re-scans all collections):

```bash
qmd update
```

### 2. Full-Text Search

Search by keyword (BM25):

```bash
qmd search "meeting notes"
```

Options:
- `--context N`: Show N lines of context around the match.
- `--all`: Show all matches in a file (default shows only the first/best one).

### 3. Semantic Search

First, ensure your notes are embedded.

#### Using Ollama (Default)

1. Ensure Ollama is running (`ollama pull nomic-embed-text`).
2. Generate embeddings:
   ```bash
   qmd embed
   ```
3. Search:
   ```bash
   qmd vsearch "decisions about architecture"
   ```

#### Using Local Inference

You can run entirely offline using `llama.cpp` libraries.

**Method A: Configuration File (Recommended)**
Edit `~/.config/qmd.yml`:
```yaml
use_local: true
local_model_path: /path/to/nomic.gguf
local_lib_path: /path/to/llamalib
```
Then run commands normally:
```bash
qmd embed
qmd vsearch "my query"
```

**Method B: CLI Flags**
Override configuration on the fly:
```bash
qmd embed --local \
  --model-path /path/to/nomic.gguf \
  --lib-path ~/lib
```

*(Tip: You can set the `YZMA_LIB` environment variable to avoid passing `--lib-path` every time.)*

### 4. MCP Server

Start the Model Context Protocol server to connect `qmd` to AI agents:

```bash
qmd server
```

#### Configuring Claude Desktop

Add the following to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "qmd": {
      "command": "/absolute/path/to/qmd",
      "args": ["server"]
    }
  }
}
```

## License

MIT
