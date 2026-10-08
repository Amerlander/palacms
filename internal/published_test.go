package internal

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

// publishedPNG is not valid UTF-8 and contains the host string, so a rewrite
// that wrongly touched binaries would be caught.
var publishedPNG = []byte("\x89PNG\r\n\x1a\n\x00\xff https://import-test.localhost/ \x00")

func seedPublishedFiles(t *testing.T, app *pocketbase.PocketBase, host string) {
	t.Helper()
	system, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer system.Close()
	files := map[string][]byte{
		"index.html":         []byte(`<a href="https://` + host + `/about/">About</a><script src="//` + host + `/_symbols/a.js"></script><a href="https://` + host + `.cdn.net/x">cdn</a>`),
		"about/index.html":   []byte(`<link rel="canonical" href="http://` + host + `/about/">`),
		"sitemap.xml":        []byte(`<urlset><url><loc>https://` + host + `/</loc></url><url><loc>https://` + host + `/about/</loc></url></urlset>`),
		"_symbols/a.js":      []byte(`fetch("https://` + host + `/data.json")`),
		"_uploads/photo.png": publishedPNG,
	}
	for name, data := range files {
		if err := system.Upload(data, "sites/"+host+"/"+name); err != nil {
			t.Fatal(err)
		}
	}
}

func readPublishedZip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = string(content)
	}
	return files
}

func TestBuildPublishedSiteZipWithoutPublicURL(t *testing.T) {
	app := newImportTestApp(t)
	defer app.ResetBootstrapState()
	site := createImportTestSite(t, app)

	if _, err := BuildPublishedSiteZip(app, site); !errors.Is(err, ErrSiteNotPublished) {
		t.Fatalf("unpublished site: got %v, want ErrSiteNotPublished", err)
	}

	host := site.GetString("host")
	seedPublishedFiles(t, app, host)
	data, err := BuildPublishedSiteZip(app, site)
	if err != nil {
		t.Fatal(err)
	}
	files := readPublishedZip(t, data)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{"_symbols/a.js", "_uploads/photo.png", "about/index.html", "index.html", "sitemap.xml"}
	if len(names) != len(want) {
		t.Fatalf("zip entries = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("zip entries = %v, want %v", names, want)
		}
	}
	if files["sitemap.xml"] != `<urlset><url><loc>https://`+host+`/</loc></url><url><loc>https://`+host+`/about/</loc></url></urlset>` {
		t.Fatalf("sitemap rewritten without public_url: %s", files["sitemap.xml"])
	}
	if files["_uploads/photo.png"] != string(publishedPNG) {
		t.Fatal("binary changed")
	}
}

func TestBuildPublishedSiteZipRewritesHostToPublicURL(t *testing.T) {
	app := newImportTestApp(t)
	defer app.ResetBootstrapState()
	site := createImportTestSite(t, app)
	host := site.GetString("host")
	seedPublishedFiles(t, app, host)
	site.Set("public_url", "https://www.example.com")

	data, err := BuildPublishedSiteZip(app, site)
	if err != nil {
		t.Fatal(err)
	}
	files := readPublishedZip(t, data)

	checks := map[string]string{
		"index.html":       `<a href="https://www.example.com/about/">About</a><script src="https://www.example.com/_symbols/a.js"></script><a href="https://` + host + `.cdn.net/x">cdn</a>`,
		"about/index.html": `<link rel="canonical" href="https://www.example.com/about/">`,
		"sitemap.xml":      `<urlset><url><loc>https://www.example.com/</loc></url><url><loc>https://www.example.com/about/</loc></url></urlset>`,
		"_symbols/a.js":    `fetch("https://www.example.com/data.json")`,
		"robots.txt":       "User-agent: *\nAllow: /\nSitemap: https://www.example.com/sitemap.xml\n",
	}
	for name, want := range checks {
		if files[name] != want {
			t.Errorf("%s = %q, want %q", name, files[name], want)
		}
	}
	if files["_uploads/photo.png"] != string(publishedPNG) {
		t.Error("binary changed")
	}
}

func TestBuildPublishedSiteZipRobots(t *testing.T) {
	app := newImportTestApp(t)
	defer app.ResetBootstrapState()
	site := createImportTestSite(t, app)
	host := site.GetString("host")
	system, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer system.Close()
	if err := system.Upload([]byte("<h1>Home</h1>"), "sites/"+host+"/index.html"); err != nil {
		t.Fatal(err)
	}
	site.Set("public_url", "https://www.example.com")

	// No sitemap: nothing to point a robots.txt at.
	data, err := BuildPublishedSiteZip(app, site)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := readPublishedZip(t, data)["robots.txt"]; ok {
		t.Fatal("robots.txt added without a sitemap")
	}

	// A published robots.txt is kept as is (with the host rewritten).
	if err := system.Upload([]byte("<urlset></urlset>"), "sites/"+host+"/sitemap.xml"); err != nil {
		t.Fatal(err)
	}
	if err := system.Upload([]byte("Sitemap: https://"+host+"/sitemap.xml\n"), "sites/"+host+"/robots.txt"); err != nil {
		t.Fatal(err)
	}
	data, err = BuildPublishedSiteZip(app, site)
	if err != nil {
		t.Fatal(err)
	}
	if got := readPublishedZip(t, data)["robots.txt"]; got != "Sitemap: https://www.example.com/sitemap.xml\n" {
		t.Fatalf("robots.txt = %q", got)
	}

	// Without public_url the zip is the plain published output.
	site.Set("public_url", "")
	if err := system.Delete("sites/" + host + "/robots.txt"); err != nil {
		t.Fatal(err)
	}
	data, err = BuildPublishedSiteZip(app, site)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := readPublishedZip(t, data)["robots.txt"]; ok {
		t.Fatal("robots.txt added without public_url")
	}
}

func TestNormalizePublicURL(t *testing.T) {
	valid := map[string]string{
		"":                          "",
		"  ":                        "",
		"https://www.example.com":   "https://www.example.com",
		"https://www.example.com/":  "https://www.example.com",
		"HTTP://Example.com/docs/":  "http://example.com/docs",
		"https://example.com:8443/": "https://example.com:8443",
	}
	for input, want := range valid {
		got, err := NormalizePublicURL(input)
		if err != nil || got != want {
			t.Errorf("NormalizePublicURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"example.com", "ftp://example.com", "https://example.com/?a=1", "https://example.com/#top", "https://user@example.com", "https:example.com", "//example.com"} {
		if got, err := NormalizePublicURL(input); err == nil {
			t.Errorf("NormalizePublicURL(%q) = %q, want error", input, got)
		}
	}
}

func TestPublicURLRequiresDeveloper(t *testing.T) {
	app := newImportTestApp(t)
	defer app.ResetBootstrapState()
	if err := RegisterPublicURLValidation(app); err != nil {
		t.Fatal(err)
	}
	site := createImportTestSite(t, app)
	router, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.OnServe().Trigger(&core.ServeEvent{App: app, Router: router}); err != nil {
		t.Fatal(err)
	}
	mux, err := router.BuildMux()
	if err != nil {
		t.Fatal(err)
	}

	users, err := app.FindCollectionByNameOrId("users")
	if err != nil {
		t.Fatal(err)
	}
	assignments, err := app.FindCollectionByNameOrId("site_role_assignments")
	if err != nil {
		t.Fatal(err)
	}
	newUser := func(email, role string) string {
		user := core.NewRecord(users)
		user.Set("email", email)
		user.SetPassword("public-url-test")
		if err := app.Save(user); err != nil {
			t.Fatal(err)
		}
		assignment := core.NewRecord(assignments)
		assignment.Set("site", site.Id)
		assignment.Set("user", user.Id)
		assignment.Set("role", role)
		if err := app.Save(assignment); err != nil {
			t.Fatal(err)
		}
		token, err := user.NewAuthToken()
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	editor := newUser("editor@example.com", "editor")
	developer := newUser("developer@example.com", "developer")

	patch := func(token, body string) int {
		req := httptest.NewRequest("PATCH", "/api/collections/sites/records/"+site.Id, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", token)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res.Code
	}

	if code := patch(editor, `{"public_url":"https://www.example.com"}`); code != 403 {
		t.Fatalf("editor change: got HTTP %d, want 403", code)
	}
	if code := patch(developer, `{"public_url":"https://www.example.com/"}`); code != 200 {
		t.Fatalf("developer change: got HTTP %d, want 200", code)
	}
	if code := patch(developer, `{"public_url":"www.example.com"}`); code != 400 {
		t.Fatalf("invalid url: got HTTP %d, want 400", code)
	}
	// Editors can still save other site fields, including an unchanged public_url.
	if code := patch(editor, `{"name":"Renamed","public_url":"https://www.example.com"}`); code != 200 {
		t.Fatalf("editor unrelated change: got HTTP %d, want 200", code)
	}

	saved, err := app.FindRecordById("sites", site.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.GetString("public_url"); got != "https://www.example.com" {
		t.Fatalf("public_url = %q, want normalized https://www.example.com", got)
	}

	// noindex is gated the same way.
	if code := patch(editor, `{"noindex":true}`); code != 403 {
		t.Fatalf("editor noindex change: got HTTP %d, want 403", code)
	}
	if code := patch(developer, `{"noindex":true}`); code != 200 {
		t.Fatalf("developer noindex change: got HTTP %d, want 200", code)
	}
	if code := patch(editor, `{"name":"Renamed again","noindex":true}`); code != 200 {
		t.Fatalf("editor unchanged noindex: got HTTP %d, want 200", code)
	}
	if saved, err = app.FindRecordById("sites", site.Id); err != nil || !saved.GetBool("noindex") {
		t.Fatalf("noindex not saved: %v", err)
	}
}
