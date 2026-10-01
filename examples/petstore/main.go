// Command petstore shows how to ship a CLI for one specific API: the spec is
// embedded in the binary and shaped with filters and transforms.
//
//	go run ./examples/petstore routes
//	go run ./examples/petstore pet list --limit 5 --status available --dry-run
//	go run ./examples/petstore pet get 42 --dry-run
package main

import (
	_ "embed"
	"time"

	"github.com/jleva12/forge-cli/auth"
	"github.com/jleva12/forge-cli/filter"
	"github.com/jleva12/forge-cli/forge"
	"github.com/jleva12/forge-cli/transform"
	"github.com/jleva12/forge-cli/transport"
)

//go:embed petstore.yaml
var specYAML []byte

// Better descriptions than the spec provides, for --help and "petstore tools".
//
//go:embed docs.yaml
var docsYAML []byte

func main() {
	docs, err := transform.DocsFromYAML(docsYAML)
	if err != nil {
		panic(err)
	}

	forge.Main(
		forge.WithName("petstore"), // env prefix: PETSTORE_ (PETSTORE_API_KEY, PETSTORE_SERVER, ...)
		forge.WithVersion("0.1.0"),
		forge.WithSpecData(specYAML),

		// Decide which routes are exposed.
		forge.WithFilters(
			filter.ExcludeTags("admin"),
			filter.ExcludeDeprecated(),
			filter.Exclude(filter.Route("DELETE /pets/*")),
		),

		// Shape the resulting commands.
		forge.WithTransforms(
			docs,
			transform.TrimGroupFromName(), // pet list-pets -> pet list, pet get-pet-by-id -> pet get-by-id
			transform.RenameOperation("getPetById", "get"),
			transform.When(filter.OperationID("healthCheck"), transform.SetGroup(""), transform.Rename("health")),
			transform.When(filter.Tag("pet"), transform.PositionalPathParams()),
			transform.ShortFlag("limit", "l"),
			transform.ParamFromEnv("X-Request-ID", "PETSTORE_REQUEST_ID"),
			transform.When(filter.OperationID("listPets"),
				transform.Aliases("ls"),
				transform.Example("  petstore pet list --status available,pending -l 10"),
			),
		),

		// "petstore auth login" saves credentials in the OS keychain.
		forge.WithCredentialStore(auth.DefaultStore(), "petstore"),

		forge.WithMiddleware(
			transport.UserAgent("petstore-cli/0.1.0"),
			transport.Retry(3, 500*time.Millisecond),
		),
	)
}
