package chat

import (
	"context"
	"fmt"

	"github.com/akhenakh/qmd/internal/mcpserver"
	"github.com/akhenakh/qmd/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

type Session struct {
	Program *tea.Program
}

func NewSession(url, modelName string, s *store.Store, mcp *mcpserver.Server) (*Session, error) {
	client := NewOllamaClient(url, modelName, mcp)
	m := initialModel(client)
	p := tea.NewProgram(m, tea.WithAltScreen()) // AltScreen for full terminal UI
	return &Session{Program: p}, nil
}

func (s *Session) Start(ctx context.Context) error {
	if _, err := s.Program.Run(); err != nil {
		return fmt.Errorf("error running chat UI: %w", err)
	}
	return nil
}
