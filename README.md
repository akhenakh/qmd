# qmd 

`qmd` is a command-line tool for indexing and searching markdown notes.  
It supports full-text search (BM25), semantic vector search, and hybrid search (RRF).  
You can generate embeddings using either an **Ollama** server or **Local Inference** (via embedded `llama.cpp` / `yzma`).

## Features

- **Full-Text Search**: Fast keyword search using BM25 with context extraction.
- **Vector Search**: Semantic search using embeddings (Ollama or Local).
- **Hybrid Search**: Combines keyword and semantic search using Reciprocal Rank Fusion (RRF) for highest accuracy.
- **Archive Support**: Index massive documentation sets directly from Zstandard compressed archives (`.zst`/`.zstd`) without decompression, compatible with [fcopy](https://github.com/akhenakh/fcopy).
- **Smart Splitting**: Uses context-aware Markdown splitting (via LangChainGo) for better embedding quality.
- **MCP Server**: Exposes search capabilities via the [Model Context Protocol](https://modelcontextprotocol.io), allowing AI assistants (like Claude Desktop) to search your notes.
- **Chat**: Interactive chat interface to query your indexed notes using natural language with context-aware responses.
- **Self-Contained**: Configuration and index are stored in a local SQLite file (`./qmd.sqlite` by default), so that you can point to different db for different purposes.

## Origin

A heavily inspired Go implementation of **Quick Markdown Search**,  [Quick Markdown Search](https://github.com/tobi/qmd), originally created by [Tobias Lütke](https://github.com/tobi).
Some opinionated differences with the original, make it a bit different, mainly all configurations and settings are self contained into the db file, making it possible to reuse the index in different places and for multiple scenarios.

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

Add one or more directories containing markdown files, or single compressed archives.

```bash
qmd add ./example ~/Documents/Obsidian ./docs/bento-v1.md.zst
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
Adds folders or archives to the index configuration.
- **Directories**: Recursively scans for `.md` files.
- **Archives**: Indexes `.zst` or `.zstd` files.
    - **Format Requirement**: The archive must contain concatenated markdown files, delimited by code blocks specifying the relative path.
      ```markdown
      ```markdown path/to/file.md
      # File Content
      ```
      ```

```bash
qmd add ~/Notes ./docs/large-docs.md.zst
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

#### `chat`
Starts an interactive chat session to query your indexed notes using natural language. The chat interface uses your indexed content to provide context-aware responses.
```bash
qmd chat
```

## MCP Server Integration

Connect `qmd` to AI agents like Claude Desktop.

### Claude
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

### Mistral Vibe

```toml
[[mcp_servers]]
name = "qmd"
transport = "stdio"
command = "/absolute/path/to/qmd"
args = [
    "server",
    "--db",
    "/absolute/path/to/qmd.sqlite",
]
prompt = "Use qmd to search through indexed markdown notes and documentation. It supports semantic search. Use the 'query' tool for most questions as it combines keyword and vector search for best results. Use 'get_document' to read the full content of a file found via search."
```

### Exposed Tools

When running as an MCP server, `qmd` exposes the following tools to the AI agent:

- **`search`**: Full-text search (BM25). Good for specific keywords.
- **`vsearch`**: Semantic vector search. Good for concepts.
- **`query`**: Hybrid search (BM25 + Vector + RRF). The most robust search method.
- **`get_document`**: Retrieves the full content of a specific file.
- **`status`**: Returns index statistics.

## License

MIT
