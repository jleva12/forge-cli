package style

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"

	"golang.org/x/term"
)

// ErrCanceled is returned by Select when the user presses Esc or Ctrl-C.
var ErrCanceled = errors.New("canceled")

// SelectItem is one choice in a Select list.
type SelectItem struct {
	// Columns are shown aligned; the first is the item's name.
	Columns []string
	// Marked items get a ● marker (e.g. the currently active one).
	Marked bool
}

// SelectOptions configures Select.
type SelectOptions struct {
	Title   string
	Items   []SelectItem
	Initial int // index highlighted at start
}

// Interactive reports whether both stdin and stdout are terminals, i.e. a
// person can answer an interactive prompt.
func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// Select shows an arrow-key menu on the terminal and returns the chosen
// item's index. Type to filter; ↑/↓ (or Ctrl-P/Ctrl-N) move; Enter selects;
// Esc or Ctrl-C cancels with ErrCanceled.
func Select(in, out *os.File, opts SelectOptions) (int, error) {
	if len(opts.Items) == 0 {
		return -1, errors.New("nothing to select")
	}
	fd := int(in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return -1, fmt.Errorf("can't read the keyboard: %w", err)
	}
	defer term.Restore(fd, state)

	m := newSelectModel(opts)
	r := &selectRenderer{out: out, p: For(out)}
	fmt.Fprint(out, "\x1b[?25l") // hide cursor
	defer fmt.Fprint(out, "\x1b[?25h")
	r.draw(m)

	buf := make([]byte, 16)
	for {
		n, err := in.Read(buf)
		if err != nil {
			r.clear()
			return -1, err
		}
		for _, k := range parseKeys(buf[:n]) {
			switch m.handle(k) {
			case selectDone:
				r.clear()
				return m.selected(), nil
			case selectCancel:
				r.clear()
				return -1, ErrCanceled
			}
		}
		r.draw(m)
	}
}

// --- keys ------------------------------------------------------------------------

type keyKind int

const (
	keyRune keyKind = iota
	keyUp
	keyDown
	keyEnter
	keyBackspace
	keyEscape
	keyCancel
	keyHome
	keyEnd
	keyOther
)

type key struct {
	kind keyKind
	r    rune
}

// parseKeys decodes raw terminal input, including arrow-key escape sequences.
func parseKeys(b []byte) []key {
	var keys []key
	for i := 0; i < len(b); {
		switch c := b[i]; {
		case c == 0x1b && i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O'):
			switch b[i+2] {
			case 'A':
				keys = append(keys, key{kind: keyUp})
			case 'B':
				keys = append(keys, key{kind: keyDown})
			case 'H':
				keys = append(keys, key{kind: keyHome})
			case 'F':
				keys = append(keys, key{kind: keyEnd})
			default:
				keys = append(keys, key{kind: keyOther})
			}
			i += 3
			// Skip the rest of longer sequences such as "\x1b[3~".
			for i < len(b) && b[i] == '~' {
				i++
			}
		case c == 0x1b:
			keys = append(keys, key{kind: keyEscape})
			i++
		case c == '\r' || c == '\n':
			keys = append(keys, key{kind: keyEnter})
			i++
		case c == 0x03 || c == 0x04: // Ctrl-C, Ctrl-D
			keys = append(keys, key{kind: keyCancel})
			i++
		case c == 0x7f || c == 0x08:
			keys = append(keys, key{kind: keyBackspace})
			i++
		case c == 0x10: // Ctrl-P
			keys = append(keys, key{kind: keyUp})
			i++
		case c == 0x0e: // Ctrl-N
			keys = append(keys, key{kind: keyDown})
			i++
		default:
			r := []rune(string(b[i:]))[0]
			if unicode.IsPrint(r) {
				keys = append(keys, key{kind: keyRune, r: r})
			}
			i += len(string(r))
		}
	}
	return keys
}

// --- model -------------------------------------------------------------------------

type selectResult int

const (
	selectContinue selectResult = iota
	selectDone
	selectCancel
)

// selectModel is the menu state, kept free of terminal I/O so it can be tested.
type selectModel struct {
	opts    SelectOptions
	query   string
	visible []int // indexes into opts.Items matching the query
	cursor  int   // position within visible
}

func newSelectModel(opts SelectOptions) *selectModel {
	m := &selectModel{opts: opts}
	m.refilter()
	for i, idx := range m.visible {
		if idx == opts.Initial {
			m.cursor = i
		}
	}
	return m
}

func (m *selectModel) refilter() {
	q := strings.ToLower(m.query)
	m.visible = m.visible[:0]
	for i, it := range m.opts.Items {
		if q == "" || strings.Contains(strings.ToLower(strings.Join(it.Columns, " ")), q) {
			m.visible = append(m.visible, i)
		}
	}
	m.cursor = min(m.cursor, max(len(m.visible)-1, 0))
}

func (m *selectModel) handle(k key) selectResult {
	switch k.kind {
	case keyUp:
		if len(m.visible) > 0 {
			m.cursor = (m.cursor - 1 + len(m.visible)) % len(m.visible)
		}
	case keyDown:
		if len(m.visible) > 0 {
			m.cursor = (m.cursor + 1) % len(m.visible)
		}
	case keyHome:
		m.cursor = 0
	case keyEnd:
		m.cursor = max(len(m.visible)-1, 0)
	case keyEnter:
		if len(m.visible) > 0 {
			return selectDone
		}
	case keyEscape, keyCancel:
		return selectCancel
	case keyBackspace:
		if m.query != "" {
			r := []rune(m.query)
			m.query = string(r[:len(r)-1])
			m.refilter()
		}
	case keyRune:
		m.query += string(k.r)
		m.cursor = 0
		m.refilter()
	}
	return selectContinue
}

func (m *selectModel) selected() int {
	if len(m.visible) == 0 {
		return -1
	}
	return m.visible[m.cursor]
}

// --- rendering -----------------------------------------------------------------------

type selectRenderer struct {
	out   io.Writer
	p     Painter
	lines int // lines drawn last time, to redraw in place
}

func (r *selectRenderer) clear() {
	if r.lines > 0 {
		fmt.Fprintf(r.out, "\x1b[%dF\x1b[J", r.lines)
		r.lines = 0
	}
}

func (r *selectRenderer) draw(m *selectModel) {
	width, height := 80, 24
	if f, ok := r.out.(*os.File); ok {
		if w, h, err := term.GetSize(int(f.Fd())); err == nil {
			width, height = w, h
		}
	}
	p := r.p
	var lines []string

	title := p.Bold(m.opts.Title)
	if m.query != "" {
		title += "  " + p.Cyan("› "+m.query)
	} else {
		title += "  " + p.Dim("type to filter")
	}
	lines = append(lines, title)

	// Align columns across all items so the layout doesn't jump while filtering.
	cols := 0
	for _, it := range m.opts.Items {
		cols = max(cols, len(it.Columns))
	}
	widths := make([]int, cols)
	for _, it := range m.opts.Items {
		for i, c := range it.Columns {
			widths[i] = max(widths[i], Width(c))
		}
	}

	// Scroll so the cursor stays in view.
	rows := min(len(m.visible), max(height-4, 3), 15)
	top := 0
	if m.cursor >= rows {
		top = m.cursor - rows + 1
	}
	if len(m.visible) == 0 {
		lines = append(lines, p.Dim("  no matches"))
	}
	for i := top; i < top+rows && i < len(m.visible); i++ {
		it := m.opts.Items[m.visible[i]]
		var cells []string
		for c, w := range widths {
			cell := ""
			if c < len(it.Columns) {
				cell = it.Columns[c]
			}
			cells = append(cells, Pad(cell, w))
		}
		body := truncate(strings.TrimRight(strings.Join(cells, "  "), " "), width-5)
		marker := " "
		if it.Marked {
			marker = p.Green("●")
		}
		if i == m.cursor {
			lines = append(lines, p.Cyan("❯ ")+marker+" "+p.Bold(p.Cyan(body)))
		} else {
			lines = append(lines, "  "+marker+" "+body)
		}
	}
	more := ""
	if len(m.visible) > rows {
		more = fmt.Sprintf(" · %d of %d", m.cursor+1, len(m.visible))
	}
	lines = append(lines, p.Dim("↑/↓ move · enter select · esc cancel"+more))

	r.clear()
	// In raw mode "\n" doesn't return the carriage, so end lines with "\r\n".
	fmt.Fprint(r.out, strings.Join(lines, "\r\n")+"\r\n")
	r.lines = len(lines)
}

// truncate shortens plain text to n columns with an ellipsis.
func truncate(s string, n int) string {
	if n <= 1 || Width(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
