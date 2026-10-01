package style

import (
	"bytes"
	"strings"
	"testing"
)

var on = Painter{on: true}

func TestJSONHighlightPreservesText(t *testing.T) {
	in := `{
  "id": 42,
  "name": "rex \"the dog\"",
  "price": -1.5e3,
  "tags": ["a", "b:c"],
  "ok": true,
  "gone": false,
  "owner": null
}
`
	out := on.JSON([]byte(in))
	if Strip(string(out)) != in {
		t.Fatalf("highlighting changed the text:\n%s", Strip(string(out)))
	}
	for _, want := range []string{
		"\x1b[1;34m\"id\"\x1b[0m",                // key
		"\x1b[32m\"rex \\\"the dog\\\"\"\x1b[0m", // string value with escapes
		"\x1b[33m-1.5e3\x1b[0m",                  // number
		"\x1b[32m\"b:c\"\x1b[0m",                 // a colon inside a string isn't a key
		"\x1b[35mtrue\x1b[0m",
		"\x1b[90mnull\x1b[0m",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if got := (Painter{}).JSON([]byte(in)); string(got) != in {
		t.Error("a disabled painter must not change output")
	}
}

func TestYAMLHighlightPreservesText(t *testing.T) {
	in := "id: 42\nowner:\n  name: joe\ntags:\n- a\n- name: x\nurl: http://example.com/a:b\n"
	if got := Strip(string(on.YAML([]byte(in)))); got != in {
		t.Fatalf("highlighting changed the text:\n%s", got)
	}
}

func TestTableAlignsColoredCells(t *testing.T) {
	var buf bytes.Buffer
	tbl := &Table{p: on, headers: []string{"METHOD", "PATH"}}
	tbl.Row(on.Method("GET"), "/pets")
	tbl.Row(on.Method("DELETE"), "/pets/{id}")
	if err := tbl.Render(&buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(Strip(buf.String())), "\n")
	want := []string{"METHOD  PATH", "GET     /pets", "DELETE  /pets/{id}"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("table = %q, want %q", lines, want)
	}
}

func TestFlagUsagesPreservesText(t *testing.T) {
	in := "  -o, --output string        output format: json|raw|yaml (default \"json\")\n" +
		"      --owner.name string    owner (required)\n" +
		"      --dry-run              print the request\n" +
		"  -h, --help                 help for x"
	out := on.FlagUsages(in)
	if Strip(out) != in {
		t.Fatalf("flag coloring changed the text:\n%s", Strip(out))
	}
	if !strings.Contains(out, on.Flag("--owner.name")) || !strings.Contains(out, on.Gray("string")) {
		t.Errorf("flags or types not colored: %q", out)
	}
}

func TestDetectMode(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("TERM", "xterm-256color")
	if m := detectMode(nil); m != Auto {
		t.Errorf("default = %v, want Auto", m)
	}
	if m := detectMode([]string{"pets", "--no-color"}); m != Never {
		t.Errorf("--no-color = %v, want Never", m)
	}
	t.Setenv("FORCE_COLOR", "1")
	if m := detectMode(nil); m != Always {
		t.Errorf("FORCE_COLOR = %v, want Always", m)
	}
	t.Setenv("NO_COLOR", "1")
	if m := detectMode(nil); m != Never {
		t.Errorf("NO_COLOR must win over FORCE_COLOR, got %v", m)
	}
}

func TestForNonTerminalIsPlain(t *testing.T) {
	SetMode(Auto)
	if For(&bytes.Buffer{}).Enabled() {
		t.Error("a buffer is not a terminal; output must be plain")
	}
}
