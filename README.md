# qmd 

A heavily inspired Go implementation of **Quick Markdown Search**,  [Quick Markdown Search](https://github.com/tobi/qmd), originally created by [Tobias Lütke](https://github.com/tobi).

`qmd` is a command-line tool for indexing and searching markdown notes.  
It supports full-text search (BM25), semantic vector search, and hybrid search (RRF).  
You can generate embeddings using either an **Ollama** server or **Local Inference** (via embedded `llama.cpp` / `yzma`).

## Features

- **Full-Text Search**: Fast keyword search using BM25 with context extraction.
- **Vector Search**: Semantic search using embeddings (Ollama or Local).
- **Hybrid Search**: Combines keyword and semantic search using Reciprocal Rank Fusion (RRF) for highest accuracy.
- **Smart Splitting**: Uses context-aware Markdown splitting (via LangChainGo) for better embedding quality.
- **MCP Server**: Exposes search capabilities via the [Model Context Protocol](https://modelcontextprotocol.io), allowing AI assistants (like Claude Desktop) to search your notes.
- **Self-Contained**: Configuration and index are stored in a local SQLite file (`./qmd.sqlite` by default), so that you can point to different db for different purposes.

## Prerequisites

1. **Go 1.25+**
2. **C Compiler** (gcc or clang) for SQLite extensions.

## Embeddings
Using embeddings is optional, qmd will default to SQLite FTS5 BM25 by default.
 
### Embeddings Option A: Ollama 

Install and run **[Ollama](https://ollama.com/)**.

### Embeddings Option B: Local Inference (Recommended for performance/control)

If you prefer to run the model directly within `qmd` without an external server, you need the `llama.cpp` shared libraries. We use [yzma](https://github.com/hybridgroup/yzma) to manage these, but you can manually download and copy the libraries from `llama.cpp` project.

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
   # Vulkan (AMD GPU)
   yzma install --lib ~/lib/yzma --processor vulkan
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

## Quick Start Workflow

`qmd` stores everything in a local database (default: `./qmd.sqlite`). You don't need to manually edit configuration files; settings are stored in the database when you run commands.

### 1. Index Notes

Add one or more directories containing markdown files.

```bash
qmd add ./example ~/Documents/Obsidian
```

### 2. Full-Text Search

You can immediately search using keywords (BM25).

```bash
qmd search "postgres slow"
```

### 3. Generate Embeddings

To enable Semantic Search (`vsearch`) and Hybrid Search (`query`), you must configure the embedding model and generate vectors. This command saves the configuration to the database for future updates.

**Using Local Inference (llama.cpp):**
```bash
qmd embed --local --model-path /opt/ml/nomic-embed-text-v1.5.Q8_0.gguf --lib-path ~/lib/yzma
```

**Or using Ollama:**
```bash
qmd embed --url http://localhost:11434 --model nomic-embed-text
```

### 4. Search

**Semantic Search (Vector only):**
```bash
qmd vsearch "performance issues with database"
```

**Hybrid Search (Best Quality):**
Combines keyword matching and semantic understanding.
```bash
qmd query "quarterly planning process"
```

## Detailed Usage

### Global Flags

- `--db <path>`: Path to the SQLite database (default `./qmd.sqlite`). Use this if you want to maintain different indexes.

### Commands

#### `add [path...]`
Adds folders to the index configuration. It recursively scans for `.md` files.
```bash
qmd add ~/Notes ~/Work/Docs
```

#### `update`
Rescans all configured collections for new or modified files.
- Updates FTS index immediately.
- If embeddings have been configured (via `qmd embed` previously), it automatically generates embeddings for new content.
```bash
qmd update
```

#### `search [query]`
Standard keyword search (BM25).
- `--context N`: Show N lines of context (default 0).
- `--all`: Show all matches in a file (default false).
```bash
qmd search "meeting" --context 2
```

#### `embed`
Configures embedding settings and generates vectors for pending documents.
- **Flags**:
    - `--local`: Enable local llama.cpp mode.
    - `--model-path`: Path to GGUF model (Local).
    - `--lib-path`: Path to llama.cpp library (Local). Can also use `YZMA_LIB` env var.
    - `--url`: Ollama URL (Default `http://localhost:11434`).
    - `--model`: Model name (Default `nomic-embed-text`).
    - `--dim`: Vector dimensions (Default `768`).

#### `vsearch [query]`
Performs cosine similarity search against generated embeddings. Requires `embed` to have been run at least once.

#### `query [query]`
Performs a hybrid search. It runs both Full-Text Search and Vector Search, then combines the results using Reciprocal Rank Fusion (RRF). This often provides better results than either method alone by balancing exact keyword matches with semantic meaning.

#### `info`
Displays current configuration, indexed collections, and database statistics.
```bash
qmd info
```

#### `server`
Starts the Model Context Protocol (MCP) server for integration with AI agents.

## MCP Server Integration

Connect `qmd` to AI agents like Claude Desktop.

**`claude_desktop_config.json`:**

```json
{
  "mcpServers": {
    "qmd": {
      "command": "/absolute/path/to/qmd",
      "args": ["server", "--db", "/absolute/path/to/qmd.sqlite"]
    }
  }
}
```

## License

MIT
