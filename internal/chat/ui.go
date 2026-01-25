package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"golang.design/x/clipboard"
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

	toolLogStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")). // Dim gray
			MarginLeft(2).
			PaddingLeft(1).
			BorderLeft(true).
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240")).
			Italic(true)

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginTop(1)

	dividerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))
)

// UI Message struct to hold structured history
type ChatMessage struct {
	Role     string
	Text     string
	ToolLogs []ToolExecutionLog
	IsError  bool
}

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

	// State
	messages     []ChatMessage
	showToolLogs bool
	lastAnswer   string
	statusMsg    string

	// History navigation
	history      []string
	historyIndex int
	historyDraft string
}

type responseMsg struct {
	content  string
	toolLogs []ToolExecutionLog
	err      error
}

type statusClearMsg struct{}

// Messages for clipboard operations
type clipboardMsg struct{}
type clipboardErrMsg struct{ err error }

func initialModel(client *OllamaClient) model {
	ti := textinput.New()
	ti.Placeholder = "Ask about your notes... (Ctrl+O: Toggle Tools, Ctrl+P: Copy)"
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
		messages:     []ChatMessage{},
		showToolLogs: false,
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
		// Re-render whole view on resize to fix word wrapping
		m.rebuildView()

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit

		// Ctrl+O: Toggle Tool Logs
		case tea.KeyCtrlO:
			m.showToolLogs = !m.showToolLogs
			m.rebuildView()
			state := "hidden"
			if m.showToolLogs {
				state = "visible"
			}
			m.statusMsg = fmt.Sprintf("Tool details %s", state)
			return m, tea.Tick(time.Second*2, func(_ time.Time) tea.Msg { return statusClearMsg{} })

		// Ctrl+P: Copy last answer
		case tea.KeyCtrlP:
			if m.lastAnswer != "" {
				m.statusMsg = "Copying to clipboard..."
				return m, copyToClipboardCmd(m.lastAnswer)
			}

		case tea.KeyEnter:
			if m.isLoading {
				return m, nil
			}
			input := strings.TrimSpace(m.textInput.Value())
			if input == "" {
				return m, nil
			}

			// Add to input history
			m.history = append(m.history, input)
			m.historyIndex = len(m.history)
			m.historyDraft = ""

			// Add to Chat Messages
			userMsg := ChatMessage{Role: "You", Text: input}
			m.messages = append(m.messages, userMsg)

			// Render just this new message (optimization)
			m.appendView(userMsg)

			m.textInput.Reset()
			m.isLoading = true
			m.statusMsg = ""

			return m, tea.Batch(
				m.spinner.Tick,
				func() tea.Msg {
					resp, logs, err := m.client.Chat(input)
					return responseMsg{content: resp, toolLogs: logs, err: err}
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
		var botMsg ChatMessage

		if msg.err != nil {
			botMsg = ChatMessage{Role: "Error", Text: msg.err.Error(), IsError: true}
		} else {
			botMsg = ChatMessage{Role: "qmd", Text: msg.content, ToolLogs: msg.toolLogs}
			m.lastAnswer = msg.content
		}

		m.messages = append(m.messages, botMsg)
		m.appendView(botMsg)
		m.textInput.Focus()
		return m, textinput.Blink

	case clipboardMsg:
		m.statusMsg = "Copied to clipboard!"
		return m, tea.Tick(time.Second*2, func(_ time.Time) tea.Msg { return statusClearMsg{} })

	case clipboardErrMsg:
		m.statusMsg = fmt.Sprintf("Clipboard error: %v", msg.err)
		return m, tea.Tick(time.Second*3, func(_ time.Time) tea.Msg { return statusClearMsg{} })

	case statusClearMsg:
		m.statusMsg = ""

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

// rebuildView clears and re-renders the entire history (used for toggles/resize)
func (m *model) rebuildView() {
	m.renderedView = ""
	for i, msg := range m.messages {
		if i > 0 {
			m.renderedView += "\n" + dividerStyle.Render(strings.Repeat("─", m.width/2)) + "\n"
		}
		m.renderMessageToView(msg)
	}
	m.viewport.SetContent(m.renderedView)
	m.viewport.GotoBottom()
}

// appendView renders a single message and appends it (used for chat flow)
func (m *model) appendView(msg ChatMessage) {
	if len(m.messages) > 1 {
		m.renderedView += "\n" + dividerStyle.Render(strings.Repeat("─", m.width/2)) + "\n"
	}
	m.renderMessageToView(msg)
	m.viewport.SetContent(m.renderedView)
	m.viewport.GotoBottom()
}

func (m *model) renderMessageToView(msg ChatMessage) {
	var style lipgloss.Style
	switch msg.Role {
	case "You":
		style = senderStyle
	case "Error":
		style = errorStyle
	default:
		style = botStyle
	}

	roleStr := style.Render(msg.Role)
	body := ""

	// Render Tool Logs if enabled
	if m.showToolLogs && len(msg.ToolLogs) > 0 {
		var toolText strings.Builder
		toolText.WriteString("\n")
		for _, log := range msg.ToolLogs {
			argsJson, _ := json.Marshal(log.Args)
			toolText.WriteString(fmt.Sprintf("🔨 %s(%s)\n", log.Name, string(argsJson)))
		}
		body += toolLogStyle.Render(toolText.String()) + "\n"
	}

	// Render Content
	if msg.Role == "qmd" {
		width := m.width - 4
		if width < 20 {
			width = 20
		}

		if msg.Text == "" {
			body += "(No content)\n"
		} else {
			renderer, err := glamour.NewTermRenderer(
				glamour.WithStandardStyle("dark"),
				glamour.WithWordWrap(width),
			)
			if err == nil {
				rendered, err := renderer.Render(msg.Text)
				if err == nil {
					body += strings.TrimSpace(rendered) + "\n"
				} else {
					body += msg.Text + "\n"
				}
			} else {
				body += msg.Text + "\n"
			}
		}
	} else {
		body += fmt.Sprintf("\n%s\n", msg.Text)
	}

	m.renderedView += fmt.Sprintf("%s\n%s", roleStr, body)
}

func (m model) View() string {
	spin := " "
	if m.isLoading {
		spin = m.spinner.View() + " Thinking..."
	} else if m.statusMsg != "" {
		spin = statusStyle.Render(m.statusMsg)
	}

	return fmt.Sprintf(
		"%s\n%s\n%s",
		m.viewport.View(),
		spin,
		m.textInput.View(),
	)
}

// copyToClipboardCmd handles clipboard copying in a non-blocking way
func copyToClipboardCmd(content string) tea.Cmd {
	return func() tea.Msg {
		if strings.TrimSpace(content) == "" {
			return clipboardErrMsg{fmt.Errorf("empty content")}
		}

		// 1. Try external tools first (often more reliable in various environments)
		tools := []string{"wl-copy", "xclip -selection clipboard", "xsel --clipboard", "pbcopy"}

		if os.Getenv("KITTY_WINDOW_ID") != "" {
			kittyPath, err := exec.LookPath("kitty")
			if err == nil {
				cmd := exec.Command(kittyPath, "+kitten", "clipboard")
				cmd.Stdin = strings.NewReader(content)
				if err := cmd.Run(); err == nil {
					return clipboardMsg{}
				}
			}
		}

		for _, tool := range tools {
			parts := strings.Fields(tool)
			path, err := exec.LookPath(parts[0])
			if err != nil {
				continue
			}

			cmd := exec.Command(path, parts[1:]...)
			cmd.Stdin = strings.NewReader(content)
			if err := cmd.Run(); err == nil {
				return clipboardMsg{}
			}
		}

		// 2. Fallback to golang.design/x/clipboard library
		// Note: Init() returns error if unavailable (e.g. missing C dependencies on Linux)
		if err := clipboard.Init(); err == nil {
			// Write returns a channel that receives struct{} when changed,
			// but we can just wait on it or assume success if no panic.
			// The library sends a signal, we wait for it to confirm write.
			select {
			case <-clipboard.Write(clipboard.FmtText, []byte(content)):
				return clipboardMsg{}
			case <-time.After(time.Second * 1):
				// Timeout if library hangs (rare but possible with Cgo)
				return clipboardErrMsg{fmt.Errorf("timeout waiting for clipboard write")}
			}
		} else {
			return clipboardErrMsg{fmt.Errorf("clipboard library init failed: %v", err)}
		}
	}
}
