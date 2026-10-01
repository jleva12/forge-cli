package style

import "testing"

func items(names ...string) []SelectItem {
	out := make([]SelectItem, len(names))
	for i, n := range names {
		out[i] = SelectItem{Columns: []string{n}}
	}
	return out
}

func TestParseKeys(t *testing.T) {
	got := parseKeys([]byte("\x1b[A\x1b[Bab\r\x7f\x1b\x03\x10\x0e\x1bOA"))
	want := []keyKind{keyUp, keyDown, keyRune, keyRune, keyEnter, keyBackspace, keyEscape, keyCancel, keyUp, keyDown, keyUp}
	if len(got) != len(want) {
		t.Fatalf("got %d keys %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i].kind != want[i] {
			t.Errorf("key %d = %v, want %v", i, got[i].kind, want[i])
		}
	}
	if got[2].r != 'a' || got[3].r != 'b' {
		t.Errorf("runes = %q %q", got[2].r, got[3].r)
	}
}

func TestSelectModelNavigation(t *testing.T) {
	m := newSelectModel(SelectOptions{Items: items("alpha", "beta", "gamma"), Initial: 1})
	if m.selected() != 1 {
		t.Fatalf("initial selection = %d, want 1", m.selected())
	}
	m.handle(key{kind: keyDown})
	m.handle(key{kind: keyDown}) // wraps around
	if m.selected() != 0 {
		t.Errorf("after 2x down from beta = %d, want 0 (wrap)", m.selected())
	}
	m.handle(key{kind: keyUp})
	if m.selected() != 2 {
		t.Errorf("up from top = %d, want 2 (wrap)", m.selected())
	}
	if m.handle(key{kind: keyEnter}) != selectDone {
		t.Error("enter should finish")
	}
	if m.handle(key{kind: keyEscape}) != selectCancel || m.handle(key{kind: keyCancel}) != selectCancel {
		t.Error("esc and ctrl-c should cancel")
	}
}

func TestSelectModelFilter(t *testing.T) {
	m := newSelectModel(SelectOptions{Items: items("petstore", "billing", "stripe-prod", "stripe-test")})
	for _, r := range "strip" {
		m.handle(key{kind: keyRune, r: r})
	}
	if len(m.visible) != 2 || m.selected() != 2 {
		t.Fatalf("filter 'strip': visible=%v selected=%d", m.visible, m.selected())
	}
	m.handle(key{kind: keyDown})
	if m.selected() != 3 {
		t.Errorf("down within filtered list = %d, want 3", m.selected())
	}
	m.handle(key{kind: keyRune, r: 'x'}) // no matches
	if m.selected() != -1 || m.handle(key{kind: keyEnter}) != selectContinue {
		t.Error("enter with no matches must not select anything")
	}
	m.handle(key{kind: keyBackspace})
	if len(m.visible) != 2 {
		t.Errorf("backspace should restore matches, visible=%v", m.visible)
	}
}
