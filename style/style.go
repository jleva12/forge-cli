// Package style adds color and layout to terminal output.
//
// Color is only used when the destination is an interactive terminal, so
// piped output (scripts, jq, AI agents) stays plain text. It can be turned
// off with --no-color or NO_COLOR, and forced on with FORCE_COLOR.
package style

import (
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/term"
)

// Mode controls whether color is used.
type Mode int

const (
	Auto   Mode = iota // color when writing to a terminal
	Always             // color even when piped
	Never              // no color
)

var (
	modeOnce sync.Once
	mode     Mode
	modeMu   sync.RWMutex
)

// SetMode overrides the mode detected from flags and the environment.
func SetMode(m Mode) {
	modeOnce.Do(func() {})
	modeMu.Lock()
	mode = m
	modeMu.Unlock()
}

func currentMode() Mode {
	modeOnce.Do(func() { mode = detectMode(os.Args[1:]) })
	modeMu.RLock()
	defer modeMu.RUnlock()
	return mode
}

// detectMode reads --no-color, NO_COLOR, FORCE_COLOR / CLICOLOR_FORCE and
// TERM=dumb. Flags are read before cobra parses them so help output, which
// skips the normal run hooks, is styled consistently.
func detectMode(args []string) Mode {
	for _, a := range args {
		if a == "--" {
			break
		}
		if a == "--no-color" || a == "--no-color=true" {
			return Never
		}
	}
	if os.Getenv("NO_COLOR") != "" {
		return Never
	}
	for _, env := range []string{"FORCE_COLOR", "CLICOLOR_FORCE"} {
		if v := os.Getenv(env); v != "" && v != "0" && v != "false" {
			return Always
		}
	}
	if os.Getenv("TERM") == "dumb" {
		return Never
	}
	return Auto
}

// Painter styles text for one destination. The zero value never colors.
type Painter struct{ on bool }

// For returns a Painter that colors only if w is a terminal (or color is forced).
func For(w io.Writer) Painter {
	switch currentMode() {
	case Never:
		return Painter{}
	case Always:
		return Painter{on: true}
	}
	f, ok := w.(*os.File)
	return Painter{on: ok && term.IsTerminal(int(f.Fd()))}
}

// Stdout and Stderr are Painters for the standard streams.
func Stdout() Painter { return For(os.Stdout) }
func Stderr() Painter { return For(os.Stderr) }

// Enabled reports whether this Painter emits color.
func (p Painter) Enabled() bool { return p.on }

func (p Painter) wrap(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p Painter) Bold(s string) string      { return p.wrap("1", s) }
func (p Painter) Dim(s string) string       { return p.wrap("2", s) }
func (p Painter) Italic(s string) string    { return p.wrap("3", s) }
func (p Painter) Underline(s string) string { return p.wrap("4", s) }
func (p Painter) Red(s string) string       { return p.wrap("31", s) }
func (p Painter) Green(s string) string     { return p.wrap("32", s) }
func (p Painter) Yellow(s string) string    { return p.wrap("33", s) }
func (p Painter) Blue(s string) string      { return p.wrap("34", s) }
func (p Painter) Magenta(s string) string   { return p.wrap("35", s) }
func (p Painter) Cyan(s string) string      { return p.wrap("36", s) }
func (p Painter) Gray(s string) string      { return p.wrap("90", s) }

// Semantic styles keep the palette consistent across commands.

func (p Painter) Heading(s string) string { return p.wrap("1;36", s) }
func (p Painter) Command(s string) string { return p.wrap("1", s) }
func (p Painter) Flag(s string) string    { return p.wrap("36", s) }
func (p Painter) Value(s string) string   { return p.wrap("33", s) }
func (p Painter) Secret(s string) string  { return p.wrap("2", s) }
func (p Painter) URL(s string) string     { return p.wrap("4;34", s) }

// Success, Warning and Failure prefix a message with a status symbol.
func (p Painter) Success(msg string) string { return p.wrap("1;32", "✓") + " " + msg }
func (p Painter) Warning(msg string) string { return p.wrap("1;33", "!") + " " + msg }
func (p Painter) Failure(msg string) string { return p.wrap("1;31", "✗") + " " + msg }
func (p Painter) Info(msg string) string    { return p.wrap("1;34", "›") + " " + msg }

// ErrorLabel is the "Error:" prefix for error messages.
func (p Painter) ErrorLabel() string { return p.wrap("1;31", "Error:") }

// Method colors an HTTP method by what it does.
func (p Painter) Method(m string) string {
	switch strings.ToUpper(m) {
	case "GET", "HEAD", "OPTIONS":
		return p.wrap("1;32", m)
	case "POST":
		return p.wrap("1;33", m)
	case "PUT", "PATCH":
		return p.wrap("1;34", m)
	case "DELETE":
		return p.wrap("1;31", m)
	}
	return p.wrap("1", m)
}

// Status colors an HTTP status line by class.
func (p Painter) Status(code int, text string) string {
	switch {
	case code >= 500:
		return p.wrap("1;31", text)
	case code >= 400:
		return p.wrap("1;33", text)
	case code >= 300:
		return p.wrap("1;36", text)
	}
	return p.wrap("1;32", text)
}

// YesNo renders a boolean as a green "yes" or a dim "no".
func (p Painter) YesNo(b bool) string {
	if b {
		return p.Green("yes")
	}
	return p.Dim("no")
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Strip removes color codes.
func Strip(s string) string { return ansi.ReplaceAllString(s, "") }

// Width is the number of terminal columns s occupies, ignoring color codes.
func Width(s string) int { return utf8.RuneCountInString(Strip(s)) }

// Pad right-pads s with spaces to width columns.
func Pad(s string, width int) string {
	if n := width - Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// TerminalWidth returns the width of w if it's a terminal, else 0.
func TerminalWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil {
			return width
		}
	}
	return 0
}
