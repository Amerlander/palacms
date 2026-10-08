package internal

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/filesystem"
)

func TestStaticSitePreview(t *testing.T) {
	for _, tc := range []struct {
		name      string
		preview   bool
		published bool
		want      string
	}{
		{"prefers stored preview", true, true, "stored preview"},
		{"preview before first publish", true, false, "stored preview"},
		{"missing preview falls back to published home", false, true, "published home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newImportTestApp(t)
			defer app.ResetBootstrapState()
			site := createImportTestSite(t, app)
			if tc.preview {
				file, err := filesystem.NewFileFromBytes([]byte("stored preview"), "index.html")
				if err != nil {
					t.Fatal(err)
				}
				site.Set("preview", file)
				if err := app.Save(site); err != nil {
					t.Fatal(err)
				}
			}
			system, err := app.NewFilesystem()
			if err != nil {
				t.Fatal(err)
			}
			defer system.Close()
			if tc.published {
				if err := system.Upload([]byte("published home"), "sites/"+site.GetString("host")+"/index.html"); err != nil {
					t.Fatal(err)
				}
			}
			if err := ServeSites(app); err != nil {
				t.Fatal(err)
			}
			router, err := apis.NewRouter(app)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.OnServe().Trigger(&core.ServeEvent{App: app, Router: router}); err != nil {
				t.Fatal(err)
			}
			handler, err := router.BuildMux()
			if err != nil {
				t.Fatal(err)
			}
			previewURL := "http://dashboard.localhost/?_site=" + site.Id + "&_preview=1&v=preview.html"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, previewURL, nil))
			if response.Code != http.StatusOK || response.Body.String() != tc.want {
				t.Fatalf("static preview: status %d, body %q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Security-Policy"); got != "frame-ancestors *; script-src 'none'" {
				t.Fatalf("preview CSP = %q", got)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("preview cache policy = %q", got)
			}

			// Asset requests don't carry query parameters; the preview URL in
			// the referrer must still resolve CSS, images and fonts to this site.
			for _, asset := range []string{"style.css", "image.png", "font.woff2"} {
				if err := system.Upload([]byte(asset), "sites/"+site.GetString("host")+"/_uploads/"+asset); err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodGet, "http://dashboard.localhost/_uploads/"+asset, nil)
				req.Header.Set("Referer", previewURL)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, req)
				if response.Code != http.StatusOK || response.Body.String() != asset {
					t.Fatalf("asset %s: status %d, body %q", asset, response.Code, response.Body.String())
				}
			}

			if tc.published {
				for _, liveURL := range []string{
					"http://dashboard.localhost/?_site=" + site.Id,
					"http://" + site.GetString("host") + "/",
				} {
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, liveURL, nil))
					if response.Code != http.StatusOK || response.Body.String() != "published home" {
						t.Fatalf("live page: status %d, body %q", response.Code, response.Body.String())
					}
					if got := response.Header().Get("Content-Security-Policy"); got != "frame-ancestors *" {
						t.Fatalf("live page CSP = %q", got)
					}
				}
			}
		})
	}
}

// TestServeSvelteRuntime covers the shared Svelte runtime under /_svelte/: JS
// content type, cache policy (hashed chunks immutable, entries revalidated),
// and preview module imports being redirected to a URL that carries `_site`.
func TestServeSvelteRuntime(t *testing.T) {
	app := newImportTestApp(t)
	defer app.ResetBootstrapState()
	site := createImportTestSite(t, app)
	system, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer system.Close()
	base := "sites/" + site.GetString("host") + "/_svelte/5.56.1/"
	for _, file := range []string{"index.js", "chunks/runtime-abc.js"} {
		if err := system.Upload([]byte(file), base+file); err != nil {
			t.Fatal(err)
		}
	}
	if err := ServeSites(app); err != nil {
		t.Fatal(err)
	}
	router, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.OnServe().Trigger(&core.ServeEvent{App: app, Router: router}); err != nil {
		t.Fatal(err)
	}
	handler, err := router.BuildMux()
	if err != nil {
		t.Fatal(err)
	}

	for file, cacheControl := range map[string]string{
		"index.js":              "no-cache",
		"chunks/runtime-abc.js": "public, max-age=31536000, immutable",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://"+site.GetString("host")+"/_svelte/5.56.1/"+file, nil))
		if response.Code != http.StatusOK || response.Body.String() != file {
			t.Fatalf("%s: status %d, body %q", file, response.Code, response.Body.String())
		}
		if got := response.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
			t.Fatalf("%s: content type %q", file, got)
		}
		if got := response.Header().Get("Cache-Control"); got != cacheControl {
			t.Fatalf("%s: cache control %q, want %q", file, got, cacheControl)
		}
	}

	// Imported from a block module in a dashboard preview: the Referer is the
	// module (no `_site` in the page URL), so the request must be redirected.
	req := httptest.NewRequest(http.MethodGet, "http://dashboard.localhost/_svelte/5.56.1/index.js", nil)
	req.Header.Set("Referer", "http://dashboard.localhost/_symbols/block.js?_site="+site.Id)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	wantLocation := "/_svelte/5.56.1/index.js?_site=" + site.Id
	if response.Code != http.StatusFound || response.Header().Get("Location") != wantLocation {
		t.Fatalf("preview import: status %d, location %q", response.Code, response.Header().Get("Location"))
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://dashboard.localhost"+wantLocation, nil))
	if response.Code != http.StatusOK || response.Body.String() != "index.js" {
		t.Fatalf("redirected preview import: status %d, body %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("preview import cache control %q", got)
	}
}
