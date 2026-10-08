package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Shared Svelte client runtime for a site's interactive blocks. The publish
// worker builds it once (a code-split ESM build) and stores it here as a single
// JSON file ({version, files: {path: code}}) instead of one record per module:
// file fields rename uploads, which would break the relative imports between
// the runtime's chunks. GenerateSite unpacks it to sites/{host}/_svelte/{version}/.
func init() {
	m.Register(
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("sites")
			if err != nil {
				return err
			}
			if collection.Fields.GetByName("svelte_runtime") == nil {
				collection.Fields.Add(&core.FileField{
					Name:      "svelte_runtime",
					MaxSelect: 1,
					MaxSize:   20 << 20,
				})
			}
			return app.Save(collection)
		},
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("sites")
			if err != nil {
				return err
			}
			if existing := collection.Fields.GetByName("svelte_runtime"); existing != nil {
				collection.Fields.RemoveById(existing.GetId())
			}
			return app.Save(collection)
		},
	)
}
