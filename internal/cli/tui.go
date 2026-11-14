package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

var errCanceled = errors.New("canceled")

type menuItem struct {
	Label  string
	Detail string
	Value  string
}

type menuOptions struct {
	Search   bool
	PageSize int
	Default  int
	Width    int
	Height   int
}

type menuScreen struct {
	lines int
}

type menuKey int

const (
	menuNone menuKey = iota
	menuRune
	menuUp
	menuDown
	menuHome
	menuEnd
	menuEnter
	menuBackspace
	menuCancel
)

type menuEvent struct {
	kind menuKey
	r    rune
}

func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func tuiAvailable() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func colorEnabled() bool {
	return tuiAvailable() && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

func ansi(code, value string) string {
	if !colorEnabled() {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func bold(value string) string   { return ansi("1", value) }
func cyan(value string) string   { return ansi("36", value) }
func dimmed(value string) string { return ansi("2", value) }

func terminalSize() (width, height int) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0
	}
	return w, h
}

func applyViewport(opts *menuOptions) {
	if opts.Width <= 0 {
		if w, _ := terminalSize(); w > 0 {
			opts.Width = w
		} else {
			opts.Width = 100
		}
	}
	if opts.Height <= 0 {
		if _, h := terminalSize(); h > 0 {
			opts.Height = h
		}
	}
}

func capPageSize(opts *menuOptions) {
	if opts.Height <= 0 {
		return
	}
	maxRows := opts.Height - 6
	if maxRows < 1 {
		maxRows = 1
	}
	if opts.PageSize > maxRows {
		opts.PageSize = maxRows
	}
}

func displayWidth(s string) int {
	width := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				if j < len(s) {
					j++
				}
			} else {
				_, size := utf8.DecodeRuneInString(s[i:])
				if size == 0 {
					size = 1
				}
				j = i + size
			}
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			size = 1
		}
		width++
		i += size
	}
	return width
}

func truncateANSI(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if displayWidth(s) <= maxWidth {
		return s
	}
	var b strings.Builder
	visible := 0
	hasANSI := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				if j < len(s) {
					j++
				}
			} else {
				_, size := utf8.DecodeRuneInString(s[i:])
				if size == 0 {
					size = 1
				}
				j = i + size
			}
			b.WriteString(s[i:j])
			hasANSI = true
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			size = 1
		}
		if visible >= maxWidth-1 {
			break
		}
		b.WriteString(s[i : i+size])
		visible++
		i += size
	}
	b.WriteRune('…')
	if hasANSI {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

func selectMenu(title string, items []menuItem, opts menuOptions) (int, error) {
	if len(items) == 0 {
		return -1, errors.New("nothing to choose from")
	}
	if !tuiAvailable() {
		return selectMenuPlain(title, items, opts)
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 12
	}
	applyViewport(&opts)
	capPageSize(&opts)

	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return -1, err
	}
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), state) }()

	s := &menuScreen{}
	fmt.Print("\x1b[?25l")
	defer fmt.Print("\x1b[?25h")

	query := ""
	cursor := 0
	if opts.Default > 0 && opts.Default < len(items) {
		cursor = opts.Default
	}
	offset := 0

	for {
		visible := filterMenuItems(items, query)
		if cursor >= len(visible) {
			cursor = len(visible) - 1
		}
		if cursor < 0 {
			cursor = 0
		}
		if cursor < offset {
			offset = cursor
		}
		if cursor >= offset+opts.PageSize {
			offset = cursor - opts.PageSize + 1
		}
		if offset < 0 {
			offset = 0
		}
		maxOffset := len(visible) - opts.PageSize
		if maxOffset < 0 {
			maxOffset = 0
		}
		if offset > maxOffset {
			offset = maxOffset
		}

		s.draw(renderMenu(title, visible, query, cursor, offset, opts))
		event, readErr := readMenuEvent(stdinReader)
		if readErr != nil {
			s.close("")
			return -1, readErr
		}

		switch event.kind {
		case menuCancel:
			s.close(dimmed("? " + title + ": canceled"))
			return -1, errCanceled
		case menuUp:
			if cursor > 0 {
				cursor--
			}
		case menuDown:
			if cursor < len(visible)-1 {
				cursor++
			}
		case menuHome:
			cursor = 0
		case menuEnd:
			if len(visible) > 0 {
				cursor = len(visible) - 1
			}
		case menuBackspace:
			if opts.Search && query != "" {
				_, size := utf8.DecodeLastRuneInString(query)
				query = query[:len(query)-size]
				cursor, offset = 0, 0
			}
		case menuRune:
			if opts.Search {
				query += string(event.r)
				cursor, offset = 0, 0
			}
		case menuEnter:
			if len(visible) == 0 {
				continue
			}
			chosen := visible[cursor]
			s.close(cyan("?") + " " + title + ": " + bold(chosen.item.Label))
			return chosen.index, nil
		}
	}
}

func selectMenuPlain(title string, items []menuItem, opts menuOptions) (int, error) {
	fmt.Println()
	fmt.Println(title + ":")
	width := 0
	for _, item := range items {
		if n := utf8.RuneCountInString(item.Label); n > width {
			width = n
		}
	}
	if width > 48 {
		width = 48
	}
	for i, item := range items {
		line := fmt.Sprintf("  %2d) %-*s", i+1, width, item.Label)
		if item.Detail != "" {
			line += "  " + item.Detail
		}
		fmt.Println(line)
	}
	def := "1"
	if opts.Default > 0 && opts.Default < len(items) {
		def = strconv.Itoa(opts.Default + 1)
	}
	line, err := promptLine("Number or value", def)
	if err != nil {
		return -1, err
	}
	if n, err := strconv.Atoi(line); err == nil {
		if n >= 1 && n <= len(items) {
			return n - 1, nil
		}
		return -1, fmt.Errorf("choice %d out of range", n)
	}
	for i, item := range items {
		if strings.EqualFold(line, item.Value) || strings.EqualFold(line, item.Label) {
			return i, nil
		}
	}
	return -1, fmt.Errorf("unknown choice %q", line)
}

func filterMenuItems(items []menuItem, query string) []indexedMenuItem {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		out := make([]indexedMenuItem, len(items))
		for i, item := range items {
			out[i] = indexedMenuItem{item: item, index: i}
		}
		return out
	}
	out := make([]indexedMenuItem, 0, len(items))
	for i, item := range items {
		haystack := strings.ToLower(item.Label + " " + item.Detail + " " + item.Value)
		if strings.Contains(haystack, query) {
			out = append(out, indexedMenuItem{item: item, index: i})
		}
	}
	return out
}

type indexedMenuItem struct {
	item  menuItem
	index int
}

func renderMenu(title string, visible []indexedMenuItem, query string, cursor, offset int, opts menuOptions) []string {
	lines := []string{cyan("?") + " " + bold(title)}
	if opts.Search && query != "" {
		lines = append(lines, "  "+dimmed("search: ")+query)
	}
	lines = append(lines, "")

	end := offset + opts.PageSize
	if end > len(visible) {
		end = len(visible)
	}
	width := menuLabelWidth(visible)
	for i := offset; i < end; i++ {
		item := visible[i].item
		marker := "  "
		label := item.Label
		if i == cursor {
			marker = cyan("❯ ")
			label = bold(label)
		}
		padding := width - utf8.RuneCountInString(item.Label)
		if padding < 2 {
			padding = 2
		}
		line := marker + label + strings.Repeat(" ", padding)
		if item.Detail != "" {
			line += dimmed(item.Detail)
		}
		lines = append(lines, line)
	}
	if len(visible) == 0 {
		lines = append(lines, "  "+dimmed("no matches"))
	}
	if len(visible) > opts.PageSize {
		first := offset + 1
		if len(visible) == 0 {
			first = 0
		}
		lines = append(lines, "  "+dimmed(fmt.Sprintf("%d-%d of %d", first, end, len(visible))))
	}
	lines = append(lines, "")
	hint := "up/down move  enter select  ctrl+c cancel"
	if opts.Search {
		hint = "up/down move  type to search  enter select  ctrl+c cancel"
	}
	lines = append(lines, "  "+dimmed(hint))

	renderWidth := opts.Width
	if renderWidth <= 0 {
		renderWidth = 100
	}
	for i := range lines {
		lines[i] = truncateANSI(lines[i], renderWidth)
	}
	return lines
}

func menuLabelWidth(items []indexedMenuItem) int {
	width := 0
	for _, item := range items {
		if n := utf8.RuneCountInString(item.item.Label); n > width {
			width = n
		}
	}
	if width > 56 {
		width = 56
	}
	return width
}

func readMenuEvent(r *bufio.Reader) (menuEvent, error) {
	b, err := r.ReadByte()
	if err != nil {
		return menuEvent{}, err
	}
	switch b {
	case 3:
		return menuEvent{kind: menuCancel}, nil
	case 13, 10:
		return menuEvent{kind: menuEnter}, nil
	case 127, 8:
		return menuEvent{kind: menuBackspace}, nil
	case 27:
		next, err := r.ReadByte()
		if err != nil {
			return menuEvent{kind: menuCancel}, nil
		}
		if next != '[' && next != 'O' {
			return menuEvent{kind: menuCancel}, nil
		}
		code, err := r.ReadByte()
		if err != nil {
			return menuEvent{}, err
		}
		switch code {
		case 'A':
			return menuEvent{kind: menuUp}, nil
		case 'B':
			return menuEvent{kind: menuDown}, nil
		case 'H':
			return menuEvent{kind: menuHome}, nil
		case 'F':
			return menuEvent{kind: menuEnd}, nil
		default:
			return menuEvent{kind: menuNone}, nil
		}
	}
	if b < 32 {
		return menuEvent{kind: menuNone}, nil
	}
	buf := []byte{b}
	for !utf8.FullRune(buf) && len(buf) < utf8.UTFMax {
		next, err := r.ReadByte()
		if err != nil {
			break
		}
		buf = append(buf, next)
	}
	value, _ := utf8.DecodeRune(buf)
	if value == utf8.RuneError && len(buf) == 1 {
		return menuEvent{kind: menuNone}, nil
	}
	return menuEvent{kind: menuRune, r: value}, nil
}

func (s *menuScreen) draw(lines []string) {
	if s.lines > 0 {
		fmt.Printf("\x1b[%dA", s.lines-1)
	}
	height := len(lines)
	if s.lines > height {
		height = s.lines
	}
	for i := 0; i < height; i++ {
		fmt.Print("\r\x1b[2K")
		if i < len(lines) {
			fmt.Print(lines[i])
		}
		if i < height-1 {
			fmt.Print("\n")
		}
	}
	s.lines = height
}

func (s *menuScreen) close(result string) {
	if s.lines > 0 {
		fmt.Printf("\x1b[%dA", s.lines-1)
	}
	for i := 0; i < s.lines; i++ {
		fmt.Print("\r\x1b[2K")
		if i < s.lines-1 {
			fmt.Print("\n")
		}
	}
	if result != "" {
		fmt.Print(result + "\n")
	}
	s.lines = 0
}
