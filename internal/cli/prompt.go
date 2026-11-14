package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"claude-custom/internal/catalog"
)

var stdinReader = bufio.NewReader(os.Stdin)

func promptLine(label, def string) (string, error) {
	if stdinIsTerminal() {
		fmt.Print(cyan("?") + " " + bold(label))
	} else {
		fmt.Print(label)
	}
	if def != "" {
		fmt.Printf(" %s", dimmed("["+def+"]"))
	}
	fmt.Print(": ")
	line, err := stdinReader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = def
	}
	if line == "" && errors.Is(err, io.EOF) {
		return "", io.EOF
	}
	return line, nil
}

func promptSecret(label string) (string, error) {
	if !stdinIsTerminal() {
		return promptLine(label, "")
	}
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return promptSecretFallback(fd, label)
	}
	defer func() { _ = term.Restore(fd, state) }()

	fmt.Print(cyan("?") + " " + bold(label) + ": ")
	return readMaskedKey(stdinReader, os.Stdout)
}

func readMaskedKey(r *bufio.Reader, w io.Writer) (string, error) {
	key := make([]byte, 0, 64)
	for {
		b, err := r.ReadByte()
		if err != nil {
			fmt.Fprint(w, "\r\n")
			return "", err
		}
		switch b {
		case 3:
			fmt.Fprint(w, "\r\n")
			return "", errCanceled
		case 13, 10:
			fmt.Fprint(w, "\r\n")
			return strings.TrimSpace(string(key)), nil
		case 8, 127:
			if len(key) > 0 {
				key = key[:len(key)-1]
				fmt.Fprint(w, "\b \b")
			}
		case 27:
			if err := discardEscapeSequence(r); err != nil {
				fmt.Fprint(w, "\r\n")
				return "", err
			}
		default:
			if b >= 32 {
				key = append(key, b)
				fmt.Fprint(w, "*")
			}
		}
	}
}

func discardEscapeSequence(r *bufio.Reader) error {
	next, err := r.Peek(1)
	if err != nil {
		return err
	}
	if next[0] != '[' && next[0] != 'O' {
		return nil
	}
	_, _ = r.ReadByte()
	for {
		b, err := r.ReadByte()
		if err != nil {
			return err
		}
		if b >= 0x40 && b <= 0x7e {
			return nil
		}
	}
}

func promptSecretFallback(fd int, label string) (string, error) {
	fmt.Print(cyan("?") + " " + bold(label) + " " + dimmed("(input hidden)") + ": ")
	b, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func promptProvider() (catalog.Preset, error) {
	items := make([]menuItem, len(catalog.Presets))
	for i, preset := range catalog.Presets {
		detail := strings.TrimPrefix(strings.TrimPrefix(preset.BaseURL, "https://"), "http://")
		detail = strings.TrimSuffix(detail, "/v1")
		if detail == "" {
			detail = "enter base URL"
		}
		items[i] = menuItem{Label: preset.Name, Detail: detail, Value: preset.ID}
	}
	index, err := selectMenu("Choose a provider", items, menuOptions{Search: true, PageSize: 14})
	if err != nil {
		return catalog.Preset{}, err
	}
	return catalog.Presets[index], nil
}

func promptModel(models []catalog.Model) (string, error) {
	return promptModelWithDefault(models, "")
}

// promptModelWithDefault offers the live model list with the profile's
// current model preselected; Enter keeps it.
func promptModelWithDefault(models []catalog.Model, current string) (string, error) {
	current = strings.TrimSpace(current)
	if len(models) == 0 {
		return promptLine("Model ID", current)
	}
	items := make([]menuItem, 0, len(models)+2)
	opts := menuOptions{Search: true, PageSize: 12}
	haveCurrent := current == ""
	for _, model := range models {
		detail := model.Name
		if strings.EqualFold(detail, model.ID) {
			detail = ""
		}
		if model.ContextWindow > 0 {
			if detail != "" {
				detail += "  "
			}
			detail += formatContext(model.ContextWindow)
		}
		if current != "" && model.ID == current {
			if detail != "" {
				detail += "  "
			}
			detail += "current"
			if !haveCurrent {
				opts.Default = len(items)
				haveCurrent = true
			}
		}
		items = append(items, menuItem{Label: model.ID, Detail: detail, Value: model.ID})
	}
	if !haveCurrent && current != "" {
		items = append(items, menuItem{Label: current, Detail: "current", Value: current})
		opts.Default = len(items) - 1
	}
	items = append(items, menuItem{Label: "Enter model ID manually", Detail: "type any model identifier", Value: "__manual__"})
	index, err := selectMenu("Choose a model (Enter keeps the current one)", items, opts)
	if err != nil {
		return "", err
	}
	if items[index].Value == "__manual__" {
		return promptLine("Model ID", current)
	}
	return items[index].Value, nil
}

func formatContext(tokens int) string {
	if tokens >= 1000 {
		if tokens%1000 == 0 {
			return fmt.Sprintf("%dk context", tokens/1000)
		}
		return fmt.Sprintf("%.1fk context", float64(tokens)/1000)
	}
	return fmt.Sprintf("%d context", tokens)
}
