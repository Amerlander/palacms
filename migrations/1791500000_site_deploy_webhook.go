package migrations

import (
	"github.com/pocketbase/pocketbase/core"
	m "github.com/pocketbase/pocketbase/migrations"
)

// A site's "Go live" webhook (see internal/deploy.go). The webhook URL, its
// headers (typically an API token) and the short-lived token that lets the
// receiver download the published zip are hidden, so they never leave the
// server through the records API. deploy_configured is the non-secret flag the
// editor uses to show the Go live button; deployed_at and deploy_status record
// the last trigger.
func init() {
	m.Register(
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("sites")
			if err != nil {
				return err
			}
			if collection.Fields.GetByName("deploy_webhook_url") != nil {
				return nil
			}
			collection.Fields.Add(
				&core.TextField{Name: "deploy_webhook_url", Max: 2000, Hidden: true},
				&core.JSONField{Name: "deploy_webhook_headers", MaxSize: 20000, Hidden: true},
				&core.BoolField{Name: "deploy_configured"},
				&core.DateField{Name: "deployed_at"},
				&core.TextField{Name: "deploy_status", Max: 20},
				&core.TextField{Name: "deploy_download_token", Max: 64, Hidden: true},
				&core.DateField{Name: "deploy_download_expires", Hidden: true},
			)
			return app.Save(collection)
		},
		func(app core.App) error {
			collection, err := app.FindCollectionByNameOrId("sites")
			if err != nil {
				return err
			}
			for _, name := range []string{"deploy_webhook_url", "deploy_webhook_headers", "deploy_configured", "deployed_at", "deploy_status", "deploy_download_token", "deploy_download_expires"} {
				if field := collection.Fields.GetByName(name); field != nil {
					collection.Fields.RemoveById(field.GetId())
				}
			}
			return app.Save(collection)
		},
	)
}
