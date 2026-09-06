package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const panelWidth = 62

type theme struct {
	enabled bool
}

func newTheme(interactive bool) theme {
	colorDisabled := os.Getenv("NO_COLOR") != "" || strings.EqualFold(os.Getenv("TERM"), "dumb")
	return theme{enabled: interactive && !colorDisabled}
}

func isTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (t theme) paint(code, text string) string {
	if !t.enabled {
		return text
	}
	return code + text + "\x1b[0m"
}

func (t theme) accent(text string) string { return t.paint("\x1b[36m", text) }
func (t theme) bold(text string) string   { return t.paint("\x1b[1m", text) }
func (t theme) dim(text string) string    { return t.paint("\x1b[2m", text) }
func (t theme) red(text string) string    { return t.paint("\x1b[31m", text) }

func (a *App) printWelcome() {
	border := strings.Repeat("─", panelWidth-2)
	fmt.Fprintln(a.out, a.theme.dim("╭"+border+"╮"))
	fmt.Fprintln(a.out, a.theme.accent(panelLine(">_ OpenAI Codex Chat")))
	fmt.Fprintln(a.out, a.theme.dim(panelLine("")))
	fmt.Fprintln(a.out, a.theme.dim(panelLine("model:     "+sanitizeTerminalText(a.model))))
	fmt.Fprintln(a.out, a.theme.dim(panelLine("directory: "+shortDirectory(a.cwd))))
	fmt.Fprintln(a.out, a.theme.dim("╰"+border+"╯"))
	fmt.Fprintln(a.out)
	fmt.Fprintf(a.out, "  %s\n", a.theme.bold("Опишите задачу для Codex"))
	fmt.Fprintf(a.out, "  %s\n\n", a.theme.dim("/help — команды · \\ в конце строки — перенос"))
}

func panelLine(text string) string {
	text = strings.ReplaceAll(sanitizeTerminalText(text), "\n", " ")
	innerWidth := panelWidth - 4
	text = fitText(text, innerWidth)
	return "│ " + text + strings.Repeat(" ", innerWidth-len([]rune(text))) + " │"
}

func fitText(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width {
		return text
	}
	if width <= 1 {
		return string(runes[:width])
	}

	prefix := (width - 1) / 2
	suffix := width - 1 - prefix
	return string(runes[:prefix]) + "…" + string(runes[len(runes)-suffix:])
}

func shortDirectory(directory string) string {
	directory = sanitizeTerminalText(directory)
	home, err := os.UserHomeDir()
	if err == nil && directory == home {
		return "~"
	}
	if err == nil && strings.HasPrefix(directory, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(directory, home)
	}
	return directory
}

func (a *App) printPrompt() {
	fmt.Fprintf(a.out, "%s ", a.theme.accent("›"))
}

func (a *App) printContinuationPrompt() {
	fmt.Fprintf(a.out, "%s ", a.theme.dim("·"))
}

func (a *App) printAnswer(answer string) {
	fmt.Fprintf(a.out, "%s %s\n", a.theme.accent("•"), a.theme.bold("codex"))
	for _, line := range strings.Split(sanitizeTerminalText(answer), "\n") {
		fmt.Fprintf(a.out, "  %s\n", line)
	}
	fmt.Fprintln(a.out)
}

func (a *App) printError(err error) {
	fmt.Fprintf(a.errOut, "%s %s\n", a.theme.red("!"), a.theme.bold("Запрос не выполнен"))
	fmt.Fprintf(a.errOut, "  %s\n\n", sanitizeTerminalText(err.Error()))
}

func (a *App) printNotice(message string) {
	fmt.Fprintf(a.out, "%s %s\n\n", a.theme.accent("•"), sanitizeTerminalText(message))
}

func (a *App) printHelp() {
	fmt.Fprintf(a.out, "\n%s\n", a.theme.bold("Команды"))
	printCommand(a.out, "/new, /reset", "начать новый диалог")
	printCommand(a.out, "/status", "показать состояние сессии")
	printCommand(a.out, "/clear", "очистить экран")
	printCommand(a.out, "/help", "показать эту справку")
	printCommand(a.out, "/exit, /quit", "завершить работу")
	fmt.Fprintf(a.out, "\n%s\n", a.theme.bold("Многострочный ввод"))
	fmt.Fprintln(a.out, "  Поставьте \\ в конце строки и продолжайте на следующей.")
	fmt.Fprintln(a.out)
}

func printCommand(out io.Writer, command, description string) {
	fmt.Fprintf(out, "  %-16s %s\n", command, description)
}

func (a *App) printStatus() {
	fmt.Fprintf(a.out, "\n%s\n", a.theme.bold("Сессия"))
	fmt.Fprintf(a.out, "  %-12s %s\n", "model", sanitizeTerminalText(a.model))
	fmt.Fprintf(a.out, "  %-12s %s\n", "directory", shortDirectory(a.cwd))
	fmt.Fprintf(a.out, "  %-12s %d\n\n", "turns", a.turns)
}

func (a *App) printUnknownCommand(command string) {
	fmt.Fprintf(a.errOut, "%s Неизвестная команда %s. Введите /help.\n\n",
		a.theme.red("!"), sanitizeTerminalText(command))
}

func (a *App) clearScreen() {
	if a.interactive {
		fmt.Fprint(a.out, "\x1b[2J\x1b[H")
	}
	a.printWelcome()
}

func (a *App) printActivity(frame string) {
	fmt.Fprintf(a.out, "\r\x1b[2K%s %s", a.theme.accent(frame), a.theme.dim("Работаю"))
}

func (a *App) clearActivity() {
	fmt.Fprint(a.out, "\r\x1b[2K")
}

// sanitizeTerminalText keeps model and API output from injecting terminal controls.
func sanitizeTerminalText(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for _, char := range text {
		if char == '\n' || char == '\t' || char >= ' ' && char != '\x7f' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}
