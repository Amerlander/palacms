package internal

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

type deployTestEnv struct {
	t         *testing.T
	app       *pocketbase.PocketBase
	mux       http.Handler
	site      *core.Record
	editor    string
	developer string
}

func newDeployTestEnv(t *testing.T) *deployTestEnv {
	t.Helper()
	app := newImportTestApp(t)
	t.Cleanup(func() { app.ResetBootstrapState() })
	if err := RegisterPublishedEndpoint(app); err != nil {
		t.Fatal(err)
	}
	if err := RegisterDeployEndpoints(app); err != nil {
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
		user.SetPassword("deploy-test")
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
	return &deployTestEnv{
		t:         t,
		app:       app,
		mux:       mux,
		site:      site,
		editor:    newUser("editor@example.com", "editor"),
		developer: newUser("developer@example.com", "developer"),
	}
}

func (env *deployTestEnv) request(method, path, token, body string) *httptest.ResponseRecorder {
	env.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	res := httptest.NewRecorder()
	env.mux.ServeHTTP(res, req)
	return res
}

func (env *deployTestEnv) reloadSite() *core.Record {
	env.t.Helper()
	site, err := env.app.FindRecordById("sites", env.site.Id)
	if err != nil {
		env.t.Fatal(err)
	}
	return site
}

func TestDeployConfigIsDeveloperOnlyAndMasked(t *testing.T) {
	env := newDeployTestEnv(t)
	path := "/api/primo/deploy-config/" + env.site.Id
	config := `{"url":"https://api.github.com/repos/o/r/dispatches","headers":[{"name":"Authorization","value":"Bearer ghp_supersecret1234"}]}`

	if res := env.request("PUT", path, env.editor, config); res.Code != 403 {
		t.Fatalf("editor PUT: got HTTP %d, want 403", res.Code)
	}
	if res := env.request("GET", path, env.editor, ""); res.Code != 403 {
		t.Fatalf("editor GET: got HTTP %d, want 403", res.Code)
	}
	if res := env.request("GET", path, "", ""); res.Code != 401 {
		t.Fatalf("anonymous GET: got HTTP %d, want 401", res.Code)
	}

	res := env.request("PUT", path, env.developer, config)
	if res.Code != 200 {
		t.Fatalf("developer PUT: got HTTP %d: %s", res.Code, res.Body)
	}
	if strings.Contains(res.Body.String(), "supersecret") {
		t.Fatalf("PUT response leaks the header value: %s", res.Body)
	}

	res = env.request("GET", path, env.developer, "")
	if res.Code != 200 {
		t.Fatalf("developer GET: got HTTP %d", res.Code)
	}
	var got struct {
		URL     string `json:"url"`
		Headers []struct {
			Name        string `json:"name"`
			ValueMasked string `json:"value_masked"`
		} `json:"headers"`
		Configured bool `json:"configured"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Body.String(), "supersecret") {
		t.Fatalf("GET response leaks the header value: %s", res.Body)
	}
	if !got.Configured || got.URL != "https://api.github.com/repos/o/r/dispatches" || len(got.Headers) != 1 || got.Headers[0].Name != "Authorization" || got.Headers[0].ValueMasked != "••••1234" {
		t.Fatalf("GET = %+v", got)
	}

	// A header sent back without a value keeps the stored one.
	if res := env.request("PUT", path, env.developer, `{"url":"https://example.com/hook","headers":[{"name":"authorization"}]}`); res.Code != 200 {
		t.Fatalf("keep value PUT: got HTTP %d: %s", res.Code, res.Body)
	}
	headers := siteDeployHeaders(env.reloadSite())
	if len(headers) != 1 || headers[0].Value != "Bearer ghp_supersecret1234" {
		t.Fatalf("stored headers = %+v", headers)
	}

	for _, body := range []string{
		`{"url":"http://example.com/hook"}`,
		`{"url":"example.com/hook"}`,
		`{"url":"https://example.com/hook","headers":[{"name":"Host","value":"x"}]}`,
		`{"url":"https://example.com/hook","headers":[{"name":"content-length","value":"1"}]}`,
		`{"url":"https://example.com/hook","headers":[{"name":"Bad Name","value":"x"}]}`,
		`{"url":"https://example.com/hook","headers":[{"name":"X-New"}]}`,
		`{"url":"https://example.com/hook","headers":[{"name":"X-A","value":"a\r\nX-B: b"}]}`,
	} {
		if res := env.request("PUT", path, env.developer, body); res.Code != 400 {
			t.Errorf("PUT %s: got HTTP %d, want 400", body, res.Code)
		}
	}

	// An empty URL clears the configuration.
	if res := env.request("PUT", path, env.developer, `{"url":""}`); res.Code != 200 {
		t.Fatalf("clear PUT: got HTTP %d", res.Code)
	}
	site := env.reloadSite()
	if site.GetBool("deploy_configured") || site.GetString("deploy_webhook_url") != "" || len(siteDeployHeaders(site)) != 0 {
		t.Fatal("configuration not cleared")
	}
}

func TestDeployFieldsHiddenFromRecordsAPI(t *testing.T) {
	env := newDeployTestEnv(t)
	path := "/api/primo/deploy-config/" + env.site.Id
	if res := env.request("PUT", path, env.developer, `{"url":"https://example.com/hook","headers":[{"name":"X-Token","value":"supersecret"}]}`); res.Code != 200 {
		t.Fatalf("PUT: got HTTP %d", res.Code)
	}
	site := env.reloadSite()
	site.Set("deploy_download_token", "downloadsecret")
	if err := env.app.Save(site); err != nil {
		t.Fatal(err)
	}

	res := env.request("GET", "/api/collections/sites/records/"+env.site.Id, env.developer, "")
	if res.Code != 200 {
		t.Fatalf("record GET: got HTTP %d", res.Code)
	}
	for _, secret := range []string{"example.com/hook", "supersecret", "downloadsecret", "deploy_webhook", "deploy_download"} {
		if strings.Contains(res.Body.String(), secret) {
			t.Fatalf("record API exposes %q: %s", secret, res.Body)
		}
	}
	if !strings.Contains(res.Body.String(), `"deploy_configured":true`) {
		t.Fatalf("deploy_configured missing: %s", res.Body)
	}

	// Members can't set the deploy state through the record API.
	if res := env.request("PATCH", "/api/collections/sites/records/"+env.site.Id, env.editor, `{"deploy_configured":false,"deploy_status":"triggered","deploy_webhook_url":"https://evil.example.com"}`); res.Code != 200 {
		t.Fatalf("record PATCH: got HTTP %d", res.Code)
	}
	site = env.reloadSite()
	if !site.GetBool("deploy_configured") || site.GetString("deploy_status") != "" || site.GetString("deploy_webhook_url") != "https://example.com/hook" {
		t.Fatal("record API changed deploy fields")
	}
}

func TestDeployTriggersWebhookAndTokenDownload(t *testing.T) {
	env := newDeployTestEnv(t)

	var gotBody map[string]any
	var gotHeader http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	deployPath := "/api/primo/deploy/" + env.site.Id
	if res := env.request("POST", deployPath, env.editor, ""); res.Code != 400 {
		t.Fatalf("unconfigured deploy: got HTTP %d, want 400", res.Code)
	}
	if res := env.request("PUT", "/api/primo/deploy-config/"+env.site.Id, env.developer, `{"url":"`+upstream.URL+`/dispatches","headers":[{"name":"Authorization","value":"Bearer secret-token"}]}`); res.Code != 200 {
		t.Fatalf("PUT: got HTTP %d: %s", res.Code, res.Body)
	}
	if res := env.request("POST", deployPath, env.editor, ""); res.Code != 409 {
		t.Fatalf("unpublished deploy: got HTTP %d, want 409", res.Code)
	}
	if res := env.request("POST", deployPath, "", ""); res.Code != 401 {
		t.Fatalf("anonymous deploy: got HTTP %d, want 401", res.Code)
	}

	seedPublishedFiles(t, env.app, env.site.GetString("host"))
	res := env.request("POST", deployPath, env.editor, "")
	if res.Code != 200 {
		t.Fatalf("deploy: got HTTP %d: %s", res.Code, res.Body)
	}

	if gotHeader.Get("Authorization") != "Bearer secret-token" || gotHeader.Get("Content-Type") != "application/json" || gotHeader.Get("User-Agent") != "primo-deploy" {
		t.Fatalf("webhook headers = %v", gotHeader)
	}
	if gotBody["event_type"] != "primo-deploy" {
		t.Fatalf("event_type = %v", gotBody["event_type"])
	}
	payload, _ := gotBody["client_payload"].(map[string]any)
	if len(payload) > 10 {
		t.Fatalf("client_payload has %d keys, GitHub allows 10", len(payload))
	}
	if payload["site_id"] != env.site.Id || payload["site_name"] != "Import Test" || payload["host"] != "import-test.localhost" || payload["triggered_by"] != "editor@example.com" {
		t.Fatalf("client_payload = %v", payload)
	}
	downloadURL, _ := payload["download_url"].(string)
	parsed, err := url.Parse(downloadURL)
	if err != nil || parsed.Path != "/api/primo/published/"+env.site.Id || len(parsed.Query().Get("token")) != 64 {
		t.Fatalf("download_url = %q", downloadURL)
	}

	site := env.reloadSite()
	if site.GetString("deploy_status") != "triggered" || site.GetDateTime("deployed_at").IsZero() {
		t.Fatalf("deploy state = %q, %v", site.GetString("deploy_status"), site.GetDateTime("deployed_at"))
	}

	// The token downloads the zip without a user session.
	download := env.request("GET", parsed.RequestURI(), "", "")
	if download.Code != 200 || download.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("token download: got HTTP %d", download.Code)
	}
	if _, ok := readPublishedZip(t, download.Body.Bytes())["index.html"]; !ok {
		t.Fatal("downloaded zip has no index.html")
	}
	if res := env.request("GET", "/api/primo/published/"+env.site.Id+"?token=wrong", "", ""); res.Code != 401 {
		t.Fatalf("wrong token: got HTTP %d, want 401", res.Code)
	}
	if res := env.request("GET", "/api/primo/published/"+env.site.Id, "", ""); res.Code != 401 {
		t.Fatalf("no token: got HTTP %d, want 401", res.Code)
	}

	site.Set("deploy_download_expires", time.Now().Add(-time.Minute))
	if err := env.app.Save(site); err != nil {
		t.Fatal(err)
	}
	if res := env.request("GET", parsed.RequestURI(), "", ""); res.Code != 401 {
		t.Fatalf("expired token: got HTTP %d, want 401", res.Code)
	}
}

func TestDeployWebhookFailure(t *testing.T) {
	env := newDeployTestEnv(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials for upstream-secret-detail"}`))
	}))
	defer upstream.Close()

	if res := env.request("PUT", "/api/primo/deploy-config/"+env.site.Id, env.developer, `{"url":"`+upstream.URL+`"}`); res.Code != 200 {
		t.Fatalf("PUT: got HTTP %d: %s", res.Code, res.Body)
	}
	seedPublishedFiles(t, env.app, env.site.GetString("host"))

	res := env.request("POST", "/api/primo/deploy/"+env.site.Id, env.editor, "")
	if res.Code != 502 {
		t.Fatalf("deploy: got HTTP %d, want 502", res.Code)
	}
	if strings.Contains(res.Body.String(), "upstream-secret-detail") {
		t.Fatalf("response leaks the upstream body: %s", res.Body)
	}
	if !strings.Contains(res.Body.String(), "401") {
		t.Fatalf("response lacks the upstream status: %s", res.Body)
	}
	site := env.reloadSite()
	if site.GetString("deploy_status") != "failed" || !site.GetDateTime("deployed_at").IsZero() {
		t.Fatalf("deploy state = %q, %v", site.GetString("deploy_status"), site.GetDateTime("deployed_at"))
	}
}
