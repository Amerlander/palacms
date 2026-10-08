package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// Keeps search engines off the Primo-served copy of a site whose live copy is
// hosted elsewhere (see internal/serve.go, which sends X-Robots-Tag for it).
// Only developers may change it (internal/published.go).
func init() {
	m.Register(
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("sites")
			if err != nil {
				return err
			}
			if collection.Fields.GetByName("noindex") != nil {
				return nil
			}
			collection.Fields.Add(&core.BoolField{Name: "noindex"})
			return app.Save(collection)
		},
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("sites")
			if err != nil {
				return err
			}
			field := collection.Fields.GetByName("noindex")
			if field == nil {
				return nil
			}
			collection.Fields.RemoveById(field.GetId())
			return app.Save(collection)
		},
	)
}
