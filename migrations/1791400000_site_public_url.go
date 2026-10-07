package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Where the deployed copy of a site lives when it's hosted outside Primo (the
// published files downloaded as a zip and put on any static host). The
// download rewrites links to the Primo host to this URL. Empty means the site
// is served by Primo itself. Validated and normalized in
// internal/published.go, since only developers may change it.
func init() {
	m.Register(
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("sites")
			if err != nil {
				return err
			}
			if collection.Fields.GetByName("public_url") != nil {
				return nil
			}
			collection.Fields.Add(&core.TextField{Name: "public_url", Max: 2000})
			return app.Save(collection)
		},
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("sites")
			if err != nil {
				return err
			}
			field := collection.Fields.GetByName("public_url")
			if field == nil {
				return nil
			}
			collection.Fields.RemoveById(field.GetId())
			return app.Save(collection)
		},
	)
}
