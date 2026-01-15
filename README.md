# qmd (Go Port)

A Go implementation of **Quick Markdown Search**, originally created by [Tobias Lütke](https://github.com/tobi).

> **Original Project:** [github.com/tobi/qmd](https://github.com/tobi/qmd)

`qmd` is a command-line tool for indexing and searching markdown notes. It supports both full-text search (using SQLite FTS5) and semantic vector search (using local LLM embeddings via Ollama).

## Features

- **Full-Text Search**: Fast keyword search using BM25.
- **Vector Search**: Semantic search using local embeddings.
- **MCP Server**: Exposes search capabilities via the [Model Context Protocol](https://modelcontextprotocol.io), allowing AI assistants (like Claude Desktop) to search your notes.
- **Local First**: All data is stored locally in `~/.cache/qmd/`.

## Prerequisites

1. **Go 1.22+**
2. **C Compiler** (gcc or clang) for SQLite extensions.
3. **[Ollama](https://ollama.com/)** running locally for vector search.

## Installation

```bash
# Clone the repository
git clone https://github.com/yourusername/qmd-go.git
cd qmd-go

# Build with FTS5 support
go build -tags sqlite_fts5 
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

### 3. Semantic Search

First, ensure Ollama is running and pull the embedding model:

```bash
ollama pull nomic-embed-text
```

Generate embeddings for your indexed documents:

```bash
qmd embed --model nomic-embed-text
```

Perform a semantic search:

```bash
qmd vsearch "decisions about architecture"
```

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

