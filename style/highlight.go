package style

import (
	"bytes"
	"regexp"
	"strings"
)

// JSON syntax-highlights (already indented) JSON: keys, strings, numbers,
// booleans and null each get their own color.
func (p Painter) JSON(data []byte) []byte {
	if !p.on {
		return data
	}
	var out bytes.Buffer
	out.Grow(len(data) * 2)
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(data) && data[j] != '"' {
				if data[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(data))
			tok := string(data[i:j])
			// A string followed by ':' is an object key.
			k := j
			for k < len(data) && (data[k] == ' ' || data[k] == '\t') {
				k++
			}
			if k < len(data) && data[k] == ':' {
				out.WriteString(p.wrap("1;34", tok))
			} else {
				out.WriteString(p.wrap("32", tok))
			}
			i = j
		case c == '-' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(data) && strings.IndexByte("0123456789.eE+-", data[j]) >= 0 {
				j++
			}
			out.WriteString(p.wrap("33", string(data[i:j])))
			i = j
		case bytes.HasPrefix(data[i:], []byte("true")):
			out.WriteString(p.wrap("35", "true"))
			i += 4
		case bytes.HasPrefix(data[i:], []byte("false")):
			out.WriteString(p.wrap("35", "false"))
			i += 5
		case bytes.HasPrefix(data[i:], []byte("null")):
			out.WriteString(p.wrap("90", "null"))
			i += 4
		case c == '{' || c == '}' || c == '[' || c == ']' || c == ',' || c == ':':
			out.WriteString(p.wrap("90", string(c)))
			i++
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.Bytes()
}

var yamlKey = regexp.MustCompile(`(?m)^(\s*(?:- )?)([^\s#:"'][^:\n]*|"[^"\n]*"|'[^'\n]*'):( |$)`)

// YAML highlights mapping keys and list markers.
func (p Painter) YAML(data []byte) []byte {
	if !p.on {
		return data
	}
	return yamlKey.ReplaceAllFunc(data, func(m []byte) []byte {
		sub := yamlKey.FindSubmatch(m)
		indent := string(sub[1])
		if strings.Contains(indent, "- ") {
			indent = strings.Replace(indent, "- ", p.Gray("- "), 1)
		}
		return []byte(indent + p.wrap("1;34", string(sub[2])) + p.Gray(":") + string(sub[3]))
	})
}

// pflag usage lines look like "  -o, --output string   description": an
// optional shorthand, the flag, an optional type, then 2+ spaces.
var flagLine = regexp.MustCompile(`^(\s+)(?:(-\S), )?(--[\w.-]+)(?: (\w+))?(\s{2,}.*|)$`)

// FlagUsages colors the flag names and value types in pflag's usage text.
func (p Painter) FlagUsages(s string) string {
	if !p.on {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		m := flagLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var b strings.Builder
		b.WriteString(m[1])
		if m[2] != "" {
			b.WriteString(p.Flag(m[2]) + ", ")
		}
		b.WriteString(p.Flag(m[3]))
		if m[4] != "" {
			b.WriteString(" " + p.Gray(m[4]))
		}
		b.WriteString(m[5])
		lines[i] = b.String()
	}
	return strings.Join(lines, "\n")
}
