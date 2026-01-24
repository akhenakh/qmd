package chat

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

var (
	senderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("5")).
			MarginTop(1).
			PaddingLeft(1)

	botStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("2")).
			MarginTop(1).
			PaddingLeft(1)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			MarginTop(1).
			PaddingLeft(1)

	dividerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))
)

type model struct {
	client       *OllamaClient
	textInput    textinput.Model
	viewport     viewport.Model
	spinner      spinner.Model
	err          error
	isLoading    bool
	renderedView string
	width        int
	height       int

	history      []string
	historyIndex int
	historyDraft string
}

type responseMsg struct {
	content string
	err     error
}

func initialModel(client *OllamaClient) model {
	ti := textinput.New()
	ti.Placeholder = "Ask about your notes..."
	ti.Focus()
	ti.CharLimit = 4096
	ti.Width = 50

	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	vp := viewport.New(0, 0)
	vp.Style = lipgloss.NewStyle().PaddingRight(2)

	return model{
		client:       client,
		textInput:    ti,
		viewport:     vp,
		spinner:      s,
		history:      []string{},
		historyIndex: 0,
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
		cmd   tea.Cmd
	)

	m.textInput, tiCmd = m.textInput.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.textInput.Width = msg.Width - 4
		vpHeight := msg.Height - 4
		if vpHeight < 0 {
			vpHeight = 0
		}
		m.viewport.Width = msg.Width
		m.viewport.Height = vpHeight

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit

		case tea.KeyEnter:
			if m.isLoading {
				return m, nil
			}
			input := strings.TrimSpace(m.textInput.Value())
			if input == "" {
				return m, nil
			}

			m.history = append(m.history, input)
			m.historyIndex = len(m.history)
			m.historyDraft = ""

			m.appendContent("You", input, senderStyle)
			m.textInput.Reset()
			m.isLoading = true

			return m, tea.Batch(
				m.spinner.Tick,
				func() tea.Msg {
					resp, err := m.client.Chat(input)
					return responseMsg{content: resp, err: err}
				},
			)

		case tea.KeyUp:
			if m.historyIndex > 0 {
				if m.historyIndex == len(m.history) {
					m.historyDraft = m.textInput.Value()
				}
				m.historyIndex--
				m.setInput(m.history[m.historyIndex])
			}

		case tea.KeyDown:
			if m.historyIndex < len(m.history) {
				m.historyIndex++
				if m.historyIndex == len(m.history) {
					m.setInput(m.historyDraft)
				} else {
					m.setInput(m.history[m.historyIndex])
				}
			}
		}

	case responseMsg:
		m.isLoading = false
		if msg.err != nil {
			m.appendContent("Error", msg.err.Error(), errorStyle)
		} else {
			m.appendContent("qmd", msg.content, botStyle)
		}
		m.textInput.Focus()
		return m, textinput.Blink

	case spinner.TickMsg:
		if m.isLoading {
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	return m, tea.Batch(tiCmd, vpCmd)
}

func (m *model) setInput(s string) {
	m.textInput.SetValue(s)
	m.textInput.SetCursor(len(s))
}

func (m *model) appendContent(role, text string, style lipgloss.Style) {
	roleStr := style.Render(role)

	var body string
	if role == "qmd" {
		width := m.width - 4
		if width < 20 {
			width = 20
		}

		if text == "" {
			text = "(No content)"
		}

		renderer, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(width),
		)

		if err == nil {
			rendered, err := renderer.Render(text)
			if err == nil {
				body = strings.TrimSpace(rendered) + "\n"
			} else {
				body = text + "\n"
			}
		} else {
			body = text + "\n"
		}
	} else {
		body = fmt.Sprintf("\n%s\n", text)
	}

	block := fmt.Sprintf("%s\n%s", roleStr, body)

	if m.renderedView != "" {
		m.renderedView += "\n" + dividerStyle.Render(strings.Repeat("─", m.width/2)) + "\n"
	}

	m.renderedView += block
	m.viewport.SetContent(m.renderedView)
	m.viewport.GotoBottom()
}

func (m model) View() string {
	spin := " "
	if m.isLoading {
		spin = m.spinner.View() + " Thinking..."
	}

	return fmt.Sprintf(
		"%s\n%s\n%s",
		m.viewport.View(),
		spin,
		m.textInput.View(),
	)
}
