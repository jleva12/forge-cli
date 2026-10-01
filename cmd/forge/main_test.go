package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runForge runs the CLI in-process against an isolated registry.
func runForge(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root, err := buildRoot(context.Background(), args)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), err
}

func setup(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FORGE_SPEC", "")
	t.Setenv("FORGE_API", "")
	t.Setenv("FORGE_CONFIG", "")
	t.Setenv("FORGE_CREDENTIAL_STORE", "file") // never touch the real keychain
	spec, err := filepath.Abs("../../examples/petstore/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestRegistryLifecycle(t *testing.T) {
	spec := setup(t)

	out, err := runForge(t, "commands")
	if err == nil || !strings.Contains(err.Error(), "forge add <name>") {
		t.Fatalf("without an API, expected setup instructions; got %v\n%s", err, out)
	}

	if out, err := runForge(t, "add", "full", spec); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := runForge(t, "add", "--name", "readonly", "--spec", spec, "--read-only", "--exclude-group", "health"); err != nil {
		t.Fatalf("add with flags: %v\n%s", err, out)
	}
	if _, err := runForge(t, "add", "full", spec); err == nil {
		t.Error("duplicate name should fail without --force")
	}
	if _, err := runForge(t, "add", "bad name", spec); err == nil {
		t.Error("invalid name should fail")
	}

	reg, err := loadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if reg.Active != "full" || len(reg.APIs) != 2 || reg.APIs["full"].Title != "Petstore 1.0.0" {
		t.Fatalf("registry = %+v", reg)
	}

	// The first API added becomes active; its write commands are present.
	out, err = runForge(t, "commands", "--required-only")
	if err != nil || !strings.Contains(out, "forge pet create-pet") {
		t.Fatalf("active=full commands: %v\n%s", err, out)
	}

	// Switching changes what's exposed.
	if _, err := runForge(t, "use", "readonly"); err != nil {
		t.Fatal(err)
	}
	out, err = runForge(t, "commands", "--required-only")
	if err != nil || strings.Contains(out, "create-pet") || strings.Contains(out, "health") {
		t.Fatalf("active=readonly should hide writes and health: %v\n%s", err, out)
	}

	// --api runs one command against another API without switching.
	out, err = runForge(t, "--api", "full", "commands", "pet", "--required-only")
	if err != nil || !strings.Contains(out, "create-pet") {
		t.Fatalf("--api full: %v\n%s", err, out)
	}
	if reg, _ := loadRegistry(); reg.Active != "readonly" {
		t.Errorf("--api must not switch the active API; active = %q", reg.Active)
	}

	if _, err := runForge(t, "use", "missing"); err == nil || !strings.Contains(err.Error(), "full, readonly") {
		t.Errorf("unknown name should list registered APIs, got %v", err)
	}

	if _, err := runForge(t, "remove", "readonly"); err != nil {
		t.Fatal(err)
	}
	if reg, _ := loadRegistry(); reg.Active != "" || len(reg.APIs) != 1 {
		t.Errorf("after removing the active API: %+v", reg)
	}
}

func TestResolvePrecedence(t *testing.T) {
	spec := setup(t)
	for _, name := range []string{"a", "b", "c"} {
		if _, err := runForge(t, "add", name, spec); err != nil {
			t.Fatal(err)
		}
	}
	// active = a (first added)
	check := func(args []string, wantName, wantSource string) {
		t.Helper()
		r, err := resolve(args)
		if err != nil {
			t.Fatal(err)
		}
		if r.name != wantName || r.source != wantSource {
			t.Errorf("resolve(%v) = %q from %q, want %q from %q", args, r.name, r.source, wantName, wantSource)
		}
	}
	check(nil, "a", "active API")
	t.Setenv("FORGE_API", "b")
	check(nil, "b", "FORGE_API")
	check([]string{"--api", "c", "commands"}, "c", "--api")
	check([]string{"--api=c"}, "c", "--api")
	check([]string{"--spec", spec, "--api", "c"}, "", "--spec")
}

func TestAddFromFile(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	specData, err := os.ReadFile("../../examples/petstore/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "pets.yaml"), specData, 0o644)
	os.WriteFile(filepath.Join(dir, "team.toml"), []byte(`
active = "pets"
[apis.pets]
spec = "pets.yaml"          # relative to this file
exclude_tags = ["admin"]
[apis.broken]
spec = "does-not-exist.yaml"
`), 0o644)

	out, err := runForge(t, "add", "--file", filepath.Join(dir, "team.toml"))
	if err == nil || !strings.Contains(out, "fail broken") || !strings.Contains(out, "add  pets") {
		t.Fatalf("expected pets added and broken reported:\nerr=%v\n%s", err, out)
	}
	reg, _ := loadRegistry()
	if reg.Active != "pets" || reg.APIs["pets"].Spec != filepath.Join(dir, "pets.yaml") || reg.APIs["broken"] != nil {
		t.Errorf("registry = %+v", reg)
	}
}

func TestReservedNamesAreNotShadowed(t *testing.T) {
	setup(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "s.yaml"), []byte(`
openapi: 3.0.3
info: {title: t, version: "1"}
servers: [{url: "https://api.test"}]
paths:
  /use:
    get: {operationId: getUse, tags: [use], responses: {"200": {description: ok}}}
`), 0o644)
	if _, err := runForge(t, "add", "x", filepath.Join(dir, "s.yaml")); err != nil {
		t.Fatal(err)
	}
	out, err := runForge(t, "routes")
	if err != nil || !strings.Contains(out, "forge use-api get-use") {
		t.Fatalf("API group named 'use' should be renamed: %v\n%s", err, out)
	}
}

func TestProfilesScopeLogins(t *testing.T) {
	spec := setup(t)
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	for _, name := range []string{"alpha", "beta"} {
		if out, err := runForge(t, "add", name, spec, "--server", srv.URL); err != nil {
			t.Fatalf("add %s: %v\n%s", name, err, out)
		}
	}
	// alpha is active (first added): log in to it.
	login(t, "alpha-token")
	runForge(t, "pet", "list-pets")

	// Switching profiles switches logins: beta has none.
	out, err := runForge(t, "use", "beta")
	if err != nil || !strings.Contains(out, "Login: none") {
		t.Fatalf("use beta: %v\n%s", err, out)
	}
	runForge(t, "pet", "list-pets")
	login(t, "beta-token")
	runForge(t, "pet", "list-pets")
	runForge(t, "--api", "alpha", "pet", "list-pets")

	want := []string{"Bearer alpha-token", "", "Bearer beta-token", "Bearer alpha-token"}
	if strings.Join(gotAuth, "|") != strings.Join(want, "|") {
		t.Errorf("Authorization per request = %q, want %q", gotAuth, want)
	}

	out, _ = runForge(t, "apis")
	if strings.Count(out, "bearer") != 2 {
		t.Errorf("apis should show both logins:\n%s", out)
	}

	// Replacing an API with a different server drops its login...
	if out, err := runForge(t, "add", "beta", spec, "--server", "https://elsewhere.example.com", "--force"); err != nil || !strings.Contains(out, "Deleted the stored login") {
		t.Errorf("add --force to a new server should delete the login: %v\n%s", err, out)
	}
	// ...while replacing it with the same endpoint keeps it.
	if out, err := runForge(t, "add", "alpha", spec, "--server", srv.URL, "--force"); err != nil || strings.Contains(out, "Deleted") {
		t.Errorf("add --force to the same endpoint should keep the login: %v\n%s", err, out)
	}
	out, _ = runForge(t, "apis")
	if strings.Count(out, "bearer") != 1 {
		t.Errorf("expected only alpha's login to remain:\n%s", out)
	}

	// --server to an unregistered host is refused with a hint.
	_, err = runForge(t, "--api", "alpha", "pet", "list-pets", "--server", "https://attacker.example.com")
	if err == nil || !strings.Contains(err.Error(), "trusted_hosts") {
		t.Errorf("expected untrusted host refusal with hint, got %v", err)
	}
}

func TestEnvPrefixCollision(t *testing.T) {
	spec := setup(t)
	if _, err := runForge(t, "add", "my-api", spec); err != nil {
		t.Fatal(err)
	}
	_, err := runForge(t, "add", "my_api", spec)
	if err == nil || !strings.Contains(err.Error(), "MY_API_*") {
		t.Fatalf("expected env prefix collision, got %v", err)
	}
	if _, err := runForge(t, "add", "my_api", spec, "--env-prefix", "MY_OTHER_API"); err != nil {
		t.Errorf("explicit --env-prefix should resolve the collision: %v", err)
	}
}

func login(t *testing.T, token string) {
	t.Helper()
	root, err := buildRoot(context.Background(), []string{"auth", "login"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader(token + "\n"))
	root.SetArgs([]string{"auth", "login", "--token-stdin"})
	if err := root.Execute(); err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}
}

func TestCompletionInstall(t *testing.T) {
	setup(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("SHELL", "/bin/zsh")
	// A symlinked dotfile, set up by hand twice, as the README used to suggest.
	dotfiles := filepath.Join(home, "dotfiles", "zshrc")
	os.MkdirAll(filepath.Dir(dotfiles), 0o755)
	os.WriteFile(dotfiles, []byte("export FOO=1\nsource <(forge completion zsh) # zsh\nsource <(forge completion zsh) # zsh\n"), 0o600)
	zshrc := filepath.Join(home, ".zshrc")
	if err := os.Symlink(dotfiles, zshrc); err != nil {
		t.Fatal(err)
	}

	out, err := runForge(t, "completion", "install")
	if err != nil || !strings.Contains(out, "Replaced zsh completion in ~/.zshrc") {
		t.Fatalf("install: %v\n%s", err, out)
	}
	first, _ := os.ReadFile(zshrc)
	want := "export FOO=1\n\n# >>> forge completion >>>\nif command -v forge >/dev/null 2>&1; then\n  source <(forge completion zsh)\nfi\n# <<< forge completion <<<\n"
	if string(first) != want {
		t.Errorf("after install:\n%s\nwant:\n%s", first, want)
	}
	if fi, err := os.Lstat(zshrc); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the .zshrc symlink should be kept")
	}
	if fi, _ := os.Stat(dotfiles); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}

	if _, err := runForge(t, "completion", "install", "zsh"); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(zshrc); string(again) != string(first) {
		t.Errorf("re-running install changed the file:\n%s", again)
	}

	if out, err := runForge(t, "completion", "uninstall"); err != nil || !strings.Contains(out, "Removed") {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if after, _ := os.ReadFile(zshrc); string(after) != "export FOO=1\n" {
		t.Errorf("after uninstall: %q", after)
	}

	// Fish gets its own file, in a directory that may not exist yet.
	if _, err := runForge(t, "completion", "install", "fish"); err != nil {
		t.Fatal(err)
	}
	fish, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "fish", "completions", "forge.fish"))
	if err != nil || !strings.Contains(string(fish), "complete -c forge") {
		t.Errorf("fish completion file: %v\n%s", err, fish)
	}

	if _, err := runForge(t, "completion", "install", "powershell"); err == nil {
		t.Error("install should reject unsupported shells")
	}
}

func TestCompletionWithoutLoadableSpec(t *testing.T) {
	setup(t)
	t.Setenv("FORGE_SPEC", filepath.Join(t.TempDir(), "missing.yaml"))
	out, err := runForge(t, "completion", "zsh")
	if err != nil {
		t.Fatalf("completion shouldn't need the spec: %v", err)
	}
	if !strings.HasPrefix(out, "#compdef forge\n(( $+functions[compdef] ))") {
		t.Errorf("zsh script should keep #compdef first, then load compinit if needed:\n%.200s", out)
	}

	for args, want := range map[string]string{
		"__complete use ":     "use",
		"__complete use":      "", // "use" is still being typed, e.g. could become "users"
		"__complete":          "",
		"__complete --api x ": "",
		"use petstore":        "use",
	} {
		if got := firstArg(strings.Split(args, " ")); got != want {
			t.Errorf("firstArg(%q) = %q, want %q", args, got, want)
		}
	}
}
