package forge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jleva12/forge-cli/auth"
	"github.com/jleva12/forge-cli/catalog"
	"github.com/jleva12/forge-cli/filter"
	"github.com/jleva12/forge-cli/forge"
	"github.com/jleva12/forge-cli/spec"
	"github.com/jleva12/forge-cli/transform"
	"github.com/jleva12/forge-cli/transport"
)

type captured struct {
	Method, Path, Query string
	Header              http.Header
	Body                string
}

func newServer(t *testing.T, status int, respBody string) (*httptest.Server, func() captured) {
	t.Helper()
	var (
		mu   sync.Mutex
		last captured
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = captured{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, func() captured { mu.Lock(); defer mu.Unlock(); return last }
}

func petstoreSpec(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../examples/petstore/petstore.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func run(t *testing.T, app *forge.App, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root, err := app.Command(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func TestRequestBuilding(t *testing.T) {
	srv, last := newServer(t, 200, `{"ok":true}`)
	t.Setenv("PETSTORE_API_KEY", "k-123")

	app := forge.New(
		forge.WithName("petstore"),
		forge.WithSpecData(petstoreSpec(t)),
		forge.WithBaseURL(srv.URL),
		forge.WithTransforms(transform.TrimGroupFromName(), transform.PositionalPathParams()),
	)

	tests := []struct {
		name  string
		args  []string
		check func(t *testing.T, c captured)
	}{
		{"query array and header", []string{"pet", "list", "--limit", "5", "--status", "available,sold", "--x-request-id", "r1"},
			func(t *testing.T, c captured) {
				assertEq(t, c.Method+" "+c.Path, "GET /pets")
				assertEq(t, c.Query, "limit=5&status=available&status=sold")
				assertEq(t, c.Header.Get("X-Request-ID"), "r1")
				assertEq(t, c.Header.Get("X-API-Key"), "k-123")
			}},
		{"positional path param", []string{"pet", "get-by-id", "42"},
			func(t *testing.T, c captured) { assertEq(t, c.Method+" "+c.Path, "GET /pets/42") }},
		{"json body from flags merged over --body", []string{"pet", "create", "--name", "rex", "--owner.name", "joe", "--tags", "a,b", "-d", `{"status":"sold"}`},
			func(t *testing.T, c captured) {
				assertJSON(t, c.Body, `{"name":"rex","owner":{"name":"joe"},"status":"sold","tags":["a","b"]}`)
				assertEq(t, c.Header.Get("Content-Type"), "application/json")
			}},
		{"typed nested body", []string{"store", "place-order", "--pet-id", "3", "--shipping.express"},
			func(t *testing.T, c captured) { assertJSON(t, c.Body, `{"petId":3,"shipping":{"express":true}}`) }},
		{"no auth when security is empty", []string{"health", "check"},
			func(t *testing.T, c captured) { assertEq(t, c.Header.Get("X-API-Key"), "") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, stderr, err := run(t, app, tt.args...); err != nil {
				t.Fatalf("run: %v\n%s", err, stderr)
			}
			tt.check(t, last())
		})
	}
}

func TestFiltersAndVendorExtensions(t *testing.T) {
	app := forge.New(
		forge.WithSpecData(petstoreSpec(t)),
		forge.WithFilters(
			filter.ExcludeTags("admin"),
			filter.ExcludeDeprecated(),
			filter.Exclude(filter.Route("DELETE /pets/*")),
		),
	)
	if err := app.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	exposed := map[string]*spec.Operation{}
	for _, op := range app.Operations() {
		exposed[op.ID] = op
	}
	for _, id := range []string{"reindex", "findPetsByTags", "deletePet", "internalMetrics"} {
		if exposed[id] != nil {
			t.Errorf("%s should be excluded", id)
		}
	}
	if got := len(app.Excluded()); got != 4 {
		t.Errorf("excluded = %d, want 4", got)
	}
	assertEq(t, exposed["uploadPetPhoto"].Name, "upload-photo")                        // x-cli-name
	assertEq(t, strings.Join(exposed["getInventory"].Aliases, ","), "inv")             // x-cli-aliases
	assertEq(t, exposed["listPets"].CommandPath(), "pet list-pets")                    // default naming
	assertEq(t, exposed["healthCheck"].CommandPath(), "health health-check")           // grouped by path
	assertEq(t, exposed["createPet"].Param("owner.name").Flag, "owner.name")           // flattened body
	assertEq(t, string(exposed["createPet"].Param("metadata").Type), string("object")) // JSON field
	if exposed["createPet"].Param("id") != nil {
		t.Error("readOnly property id should not be a flag")
	}
}

func TestHTTPErrorAndOutput(t *testing.T) {
	srv, _ := newServer(t, 404, `{"error":"not found"}`)
	app := forge.New(forge.WithSpecData(petstoreSpec(t)), forge.WithBaseURL(srv.URL))

	stdout, stderr, err := run(t, app, "pet", "get-pet-by-id", "--pet-id", "1", "-o", "yaml")
	var httpErr *forge.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != 404 {
		t.Fatalf("err = %v, want HTTPError 404", err)
	}
	assertEq(t, stdout, "")
	assertEq(t, stderr, "error: not found\n")
}

func TestRequiredParamsAndEnum(t *testing.T) {
	app := forge.New(forge.WithSpecData(petstoreSpec(t)), forge.WithBaseURL("http://unused"))
	if _, _, err := run(t, app, "pet", "get-pet-by-id"); err == nil || !strings.Contains(err.Error(), "--pet-id") {
		t.Errorf("missing path param: err = %v", err)
	}
	if _, _, err := run(t, app, "pet", "list-pets", "--status", "bogus"); err == nil || !strings.Contains(err.Error(), "one of") {
		t.Errorf("enum: err = %v", err)
	}
}

func TestDryRunAndCustomAuth(t *testing.T) {
	var hookSaw string
	app := forge.New(
		forge.WithSpecData(petstoreSpec(t)),
		forge.WithBaseURL("https://api.test"),
		forge.WithAuth("bearerAuth", auth.Bearer{Token: auth.Static("tok")}),
		forge.WithRequestHook(func(_ context.Context, op *spec.Operation, req *http.Request) error {
			hookSaw = op.ID
			req.Header.Set("X-Signed", "yes")
			return nil
		}),
	)
	stdout, _, err := run(t, app, "store", "get-inventory", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	assertEq(t, hookSaw, "getInventory")
	for _, want := range []string{"curl -X GET 'https://api.test/store/inventory'", "'Authorization: REDACTED'", "'X-Signed: yes'"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, stdout)
		}
	}
}

func TestDryRunIncludesMiddleware(t *testing.T) {
	sent := false
	app := forge.New(
		forge.WithSpecData(petstoreSpec(t)),
		forge.WithBaseURL("https://api.test"),
		forge.WithHTTPClient(&http.Client{Transport: transport.RoundTripperFunc(func(*http.Request) (*http.Response, error) {
			sent = true
			return nil, errors.New("dry run must not reach the network transport")
		})}),
		forge.WithMiddleware(transport.UserAgent("petstore-cli/0.1.0"), transport.Header("X-Api-Key", "s3cret")),
	)
	stdout, _, err := run(t, app, "pet", "list-pets", "--dry-run", "-v")
	if err != nil {
		t.Fatal(err)
	}
	if sent {
		t.Error("request was sent")
	}
	for _, want := range []string{"'User-Agent: petstore-cli/0.1.0'", "'X-Api-Key: REDACTED'"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Count(stdout, "curl ") != 1 {
		t.Errorf("expected exactly one curl command:\n%s", stdout)
	}
}

func TestToolsAndCall(t *testing.T) {
	srv, last := newServer(t, 201, `{"id": 7}`)
	app := forge.New(
		forge.WithName("petstore"),
		forge.WithSpecData(petstoreSpec(t)),
		forge.WithBaseURL(srv.URL),
		forge.WithFilters(filter.ExcludeTags("admin")),
		forge.WithTransforms(
			transform.TrimGroupFromName(),
			transform.When(filter.OperationID("createPet"), transform.Description("Adds a pet to the store.")),
			transform.DescribeParam("owner.name", "Owner's full name"),
		),
	)

	stdout, _, err := run(t, app, "tools", "pet", "create")
	if err != nil {
		t.Fatal(err)
	}
	var cat forge.Catalog
	if err := json.Unmarshal([]byte(stdout), &cat); err != nil {
		t.Fatalf("catalog is not JSON: %v\n%s", err, stdout)
	}
	if len(cat.Tools) != 1 || cat.Tools[0].Name != "pet_create" {
		t.Fatalf("tools = %+v", cat.Tools)
	}
	tool := cat.Tools[0]
	if !strings.Contains(tool.Description, "Adds a pet to the store.") {
		t.Errorf("description override missing: %q", tool.Description)
	}
	owner := tool.InputSchema["properties"].(map[string]any)["owner.name"].(map[string]any)
	assertEq(t, owner["description"].(string), "Owner's full name.")

	// Everything in the catalog must be callable with matching input.
	if _, stderr, err := run(t, app, "call", "pet_create", `{"name":"rex","owner.name":"joe","tags":["a"],"metadata":{"k":1}}`); err != nil {
		t.Fatalf("call: %v\n%s", err, stderr)
	}
	c := last()
	assertEq(t, c.Method+" "+c.Path, "POST /pets")
	assertJSON(t, c.Body, `{"name":"rex","owner":{"name":"joe"},"tags":["a"],"metadata":{"k":1}}`)

	if _, _, err := run(t, app, "call", "pet_get_by_id", `{"pet-id": 9007199254740993}`); err != nil {
		t.Fatal(err)
	}
	assertEq(t, last().Path, "/pets/9007199254740993") // no float64 precision loss

	for input, wantErr := range map[string]string{
		`{"limt": 5}`:           "unknown input property: limt",
		`{"status": ["bogus"]}`: "one of",
		`{"limit": "many"}`:     "expected integer",
		`[1,2]`:                 "must be a JSON object",
	} {
		if _, _, err := run(t, app, "call", "pet_list", input); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("call pet_list %s: err = %v, want %q", input, err, wantErr)
		}
	}
	if _, _, err := run(t, app, "call", "pet_lst"); err == nil || !strings.Contains(err.Error(), "did you mean: pet_list") {
		t.Errorf("typo suggestion: err = %v", err)
	}

	stdout, _, err = run(t, app, "tools", "--format", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	var anthropic []map[string]any
	if err := json.Unmarshal([]byte(stdout), &anthropic); err != nil || len(anthropic) != len(app.Tools(catalog.Options{})) {
		t.Errorf("anthropic format: %v, %d tools", err, len(anthropic))
	}
}

func TestCommandsTable(t *testing.T) {
	app := forge.New(
		forge.WithName("petstore"),
		forge.WithSpecData(petstoreSpec(t)),
		forge.WithTransforms(
			transform.TrimGroupFromName(),
			transform.When(filter.Tag("pet"), transform.PositionalPathParams()),
			transform.ParamFromEnv("X-Request-ID", "REQ_ID"),
		),
	)
	stdout, _, err := run(t, app, "commands", "pet")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"petstore pet list [--limit <integer>] [--status <available|pending|sold>,...] [--x-request-id <string>]",
		"petstore pet create --name <string> [--metadata <json>]",
		"[--tags <string>,...] [-d <json|@file|->]",
		"petstore pet get-by-id <pet-id>\n",
		"petstore pet upload-photo <pet-id> [--caption <string>] [--file <file>]",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("commands output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "petstore store ") || strings.Contains(stdout, "petstore health") {
		t.Errorf("group filter leaked other groups:\n%s", stdout)
	}

	stdout, _, err = run(t, app, "commands", "pet", "--required-only")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, " [-") || !strings.Contains(stdout, "petstore pet create --name <string>\n") {
		t.Errorf("--required-only output:\n%s", stdout)
	}
}

func TestLoginStoresAndAppliesCredentials(t *testing.T) {
	srv, last := newServer(t, 200, `{}`)
	store := &auth.FileStore{Path: t.TempDir() + "/creds.json"}
	t.Setenv("PETSTORE_API_KEY", "")
	t.Setenv("PETSTORE_TOKEN", "")
	t.Setenv("PETSTORE_BEARER_AUTH", "")
	newApp := func() *forge.App {
		return forge.New(
			forge.WithName("petstore"),
			forge.WithSpecData(petstoreSpec(t)),
			forge.WithBaseURL(srv.URL),
			forge.WithCredentialStore(store, "petstore"),
		)
	}
	login := func(stdin string, args ...string) string {
		t.Helper()
		root, err := newApp().Command(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetIn(strings.NewReader(stdin))
		root.SetArgs(append([]string{"auth", "login"}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("login %v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}

	// The spec offers apiKey and bearerAuth; bearer is inferred.
	out := login("tok-123\n", "--token-stdin")
	if !strings.Contains(out, "scheme bearerAuth") || strings.Contains(out, "tok-123") {
		t.Errorf("login output should name the scheme and never echo the secret:\n%s", out)
	}
	if _, _, err := run(t, newApp(), "pet", "list-pets"); err != nil {
		t.Fatal(err)
	}
	assertEq(t, last().Header.Get("Authorization"), "Bearer tok-123")

	// The login is bound to the API's host, and auth status checks it there.
	status, _, _ := run(t, newApp(), "auth", "status")
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "bearerAuth ") && !strings.Contains(line, " yes ") {
			t.Errorf("auth status should report the stored login as configured:\n%s", status)
		}
	}

	// Explicitly anonymous operations ("security: []") never get credentials.
	if _, _, err := run(t, newApp(), "health", "health-check"); err != nil {
		t.Fatal(err)
	}
	assertEq(t, last().Header.Get("Authorization"), "")

	// The apiKey scheme sends the key in the header named by the spec.
	login("key-456\n", "--scheme", "apiKey", "--token-stdin")
	run(t, newApp(), "pet", "list-pets")
	assertEq(t, last().Header.Get("X-API-Key"), "key-456")
	assertEq(t, last().Header.Get("Authorization"), "")

	// Environment variables take precedence over the stored login.
	t.Setenv("PETSTORE_API_KEY", "from-env")
	run(t, newApp(), "pet", "list-pets")
	assertEq(t, last().Header.Get("X-API-Key"), "from-env")
	t.Setenv("PETSTORE_API_KEY", "")

	// A custom header with a prefix, not tied to a spec scheme.
	login("789\n", "--header", "Authorization", "--prefix", "Token ", "--token-stdin")
	run(t, newApp(), "pet", "list-pets")
	assertEq(t, last().Header.Get("Authorization"), "Token 789")

	stdout, _, err := run(t, newApp(), "auth", "status")
	if err != nil || !strings.Contains(stdout, "header Authorization: Token") || strings.Contains(stdout, "789") {
		t.Errorf("auth status should describe the stored login without the secret: %v\n%s", err, stdout)
	}

	// A login saved for one scheme isn't sent to operations that use another.
	login("key-456\n", "--scheme", "apiKey", "--token-stdin")
	spec2 := strings.Replace(string(petstoreSpec(t)), "security:\n  - apiKey: []\n  - bearerAuth: []", "security:\n  - bearerAuth: []", 1)
	strict := forge.New(forge.WithSpecData([]byte(spec2)), forge.WithBaseURL(srv.URL), forge.WithCredentialStore(store, "petstore"))
	run(t, strict, "pet", "list-pets")
	assertEq(t, last().Header.Get("X-API-Key"), "")

	// Specs without security schemes use the login for everything.
	noAuth := strings.Replace(spec2, "security:\n  - bearerAuth: []", "", 1)
	noAuth = noAuth[:strings.Index(noAuth, "  securitySchemes:")] + noAuth[strings.Index(noAuth, "  schemas:"):]
	open := forge.New(forge.WithName("petstore"), forge.WithSpecData([]byte(noAuth)), forge.WithBaseURL(srv.URL), forge.WithCredentialStore(store, "petstore"))
	root, _ := open.Command(context.Background())
	root.SetIn(strings.NewReader("anything\n"))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"auth", "login", "--token-stdin"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	run(t, open, "pet", "list-pets")
	assertEq(t, last().Header.Get("Authorization"), "Bearer anything")

	if _, _, err := run(t, newApp(), "auth", "logout"); err != nil {
		t.Fatal(err)
	}
	run(t, newApp(), "pet", "list-pets")
	assertEq(t, last().Header.Get("Authorization"), "")
}

func TestCredentialsNeverLeaveTrustedHosts(t *testing.T) {
	api, apiLast := newServer(t, 200, `{}`)
	evil, evilLast := newServer(t, 200, `{}`)
	store := &auth.FileStore{Path: t.TempDir() + "/creds.json"}
	t.Setenv("PETSTORE_API_KEY", "")
	t.Setenv("PETSTORE_TOKEN", "")
	newApp := func(opts ...forge.Option) *forge.App {
		return forge.New(append([]forge.Option{
			forge.WithName("petstore"), forge.WithSpecData(petstoreSpec(t)),
			forge.WithBaseURL(api.URL), forge.WithCredentialStore(store, "petstore"),
		}, opts...)...)
	}
	root, _ := newApp().Command(context.Background())
	root.SetIn(strings.NewReader("secret-token\n"))
	root.SetOut(io.Discard)
	root.SetArgs([]string{"auth", "login", "--token-stdin"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	// The login works against its own server.
	if _, _, err := run(t, newApp(), "pet", "list-pets"); err != nil {
		t.Fatal(err)
	}
	assertEq(t, apiLast().Header.Get("Authorization"), "Bearer secret-token")

	// --server pointing elsewhere is refused before anything is sent.
	_, _, err := run(t, newApp(), "pet", "list-pets", "--server", evil.URL)
	if err == nil || !strings.Contains(err.Error(), "refusing to send") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if evilLast().Method != "" {
		t.Fatal("request reached the untrusted host")
	}

	// Environment-variable credentials are protected the same way.
	t.Setenv("PETSTORE_API_KEY", "env-key")
	if _, _, err := run(t, newApp(), "pet", "list-pets", "--server", evil.URL); err == nil {
		t.Error("env credentials must not go to an untrusted host")
	}
	t.Setenv("PETSTORE_API_KEY", "")

	// Explicitly trusted hosts work, but a login bound to the old host is
	// not silently reused there: it asks for a new login.
	trusted := newApp(forge.WithTrustedHosts(strings.TrimPrefix(evil.URL, "http://")))
	_, _, err = run(t, trusted, "pet", "list-pets", "--server", evil.URL)
	if err == nil || !strings.Contains(err.Error(), "bound to") {
		t.Errorf("expected host-bound login error, got %v", err)
	}

	// A profile whose server changed after login doesn't leak the token either.
	moved := forge.New(forge.WithName("petstore"), forge.WithSpecData(petstoreSpec(t)),
		forge.WithBaseURL(evil.URL), forge.WithCredentialStore(store, "petstore"))
	if _, _, err := run(t, moved, "pet", "list-pets"); err == nil || !strings.Contains(err.Error(), "bound to") {
		t.Errorf("expected host-bound login error after server change, got %v", err)
	}
	if evilLast().Method != "" {
		t.Fatal("request reached the other host")
	}

	// Requests without credentials may still go anywhere (e.g. public endpoints on a mock server).
	if _, _, err := run(t, newApp(), "health", "health-check", "--server", evil.URL); err != nil {
		t.Errorf("unauthenticated request to another host should be allowed: %v", err)
	}
}

func TestDocsOverlay(t *testing.T) {
	docs, err := transform.DocsFromYAML([]byte(`
listPets:
  summary: Browse pets
  params:
    limit: Page size
"GET /store/inventory":
  description: Counts of pets by status.
`))
	if err != nil {
		t.Fatal(err)
	}
	app := forge.New(forge.WithSpecData(petstoreSpec(t)), forge.WithTransforms(docs))
	if err := app.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, op := range app.Operations() {
		switch op.ID {
		case "listPets":
			assertEq(t, op.Summary, "Browse pets")
			assertEq(t, op.Param("limit").Description, "Page size")
		case "getInventory":
			assertEq(t, op.Description, "Counts of pets by status.")
		}
	}
}

func TestSwagger2(t *testing.T) {
	doc := `{"swagger":"2.0","info":{"title":"t","version":"1"},"host":"api.test","basePath":"/v2","schemes":["https"],
	  "paths":{"/users/{id}":{"get":{"operationId":"getUser","tags":["users"],
	    "parameters":[{"name":"id","in":"path","required":true,"type":"integer"}],"responses":{"200":{"description":"ok"}}}}}}`
	app := forge.New(forge.WithSpecData([]byte(doc)))
	stdout, _, err := run(t, app, "users", "get-user", "--id", "7", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "'https://api.test/v2/users/7'") {
		t.Errorf("unexpected: %s", stdout)
	}
}

func assertEq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func assertJSON(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("invalid JSON %q: %v", got, err)
	}
	json.Unmarshal([]byte(want), &w)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	assertEq(t, string(gb), string(wb))
}

func TestOpenAPI31FileUpload(t *testing.T) {
	srv, last := newServer(t, 201, `{}`)
	doc := `{"openapi": "3.1.0", "info": {"title": "t", "version": "1"},
  "paths": {"/templates": {"post": {"operationId": "createTemplate",
    "requestBody": {"required": true, "content": {"multipart/form-data": {"schema": {
      "type": "object", "required": ["file", "name"],
      "properties": {"file": {"type": "string", "contentMediaType": "application/octet-stream"}, "name": {"type": "string"}}}}}},
    "responses": {"201": {"description": "created"}}}}}}`
	path := t.TempDir() + "/page.html"
	os.WriteFile(path, []byte("<h1>{{ title }}</h1>"), 0o644)

	app := forge.New(forge.WithSpecData([]byte(doc)), forge.WithBaseURL(srv.URL))
	if err := app.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	args := append(strings.Fields(app.Operations()[0].CommandPath()), "--file", path, "--name", "demo")
	if _, stderr, err := run(t, app, args...); err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	if body := last().Body; !strings.Contains(body, `filename="page.html"`) || !strings.Contains(body, "<h1>{{ title }}</h1>") {
		t.Errorf("the file's contents should be uploaded, not its path:\n%s", body)
	}
}
