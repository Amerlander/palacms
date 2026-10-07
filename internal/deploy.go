package internal

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
	"golang.org/x/net/http/httpguts"
)

// "Go live" hands a site's published output to whatever deploys it elsewhere.
// Primo doesn't know about hosting providers: a developer configures one
// webhook per site, and Go live POSTs a repository_dispatch-compatible body to
// it with a short-lived link to the published zip (published.go). That body
// can go straight to GitHub's dispatches API, so a GitHub Action can fetch the
// zip and deploy it anywhere.

const (
	deployDownloadTTL  = time.Hour
	deployTimeout      = 15 * time.Second
	deployMaxHeaders   = 10
	deployStatusOK     = "triggered"
	deployStatusFailed = "failed"
	// PocketBase's built-in Meta.AppURL. It's never where a CI runner can reach
	// the server, so it doesn't count as configured.
	defaultPocketBaseAppURL = "http://localhost:8090"
)

// Set by Primo on every webhook request, or meaningless to override.
var reservedDeployHeaders = map[string]bool{
	"Host":           true,
	"Content-Length": true,
	"Content-Type":   true,
}

// Fields only the deploy endpoints write. The record API would otherwise let
// any site member flip deploy_configured or fake a deploy status.
var deployStateFields = []string{"deploy_configured", "deployed_at", "deploy_status"}

type deployHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func siteDeployHeaders(site *core.Record) []deployHeader {
	var headers []deployHeader
	site.UnmarshalJSONField("deploy_webhook_headers", &headers)
	return headers
}

// clearSiteDeploy removes a site's go-live webhook and download token.
func clearSiteDeploy(site *core.Record) {
	site.Set("deploy_webhook_url", "")
	site.Set("deploy_webhook_headers", nil)
	site.Set("deploy_configured", false)
	site.Set("deploy_download_token", "")
	site.Set("deploy_download_expires", "")
}

// normalizeDeployWebhookURL accepts an https URL, or http for the local
// machine only: the request carries a bearer token, which must not travel in
// the clear.
func normalizeDeployWebhookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	invalid := errors.New("Webhook URL must be an absolute https URL")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "", invalid
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		hostname := u.Hostname()
		ip := net.ParseIP(hostname)
		if hostname != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", invalid
		}
	default:
		return "", invalid
	}
	return u.String(), nil
}

// maskDeployHeaderValue lets a developer recognize a stored secret without
// reading it back.
func maskDeployHeaderValue(value string) string {
	runes := []rune(value)
	if len(runes) <= 8 {
		return "••••"
	}
	return "••••" + string(runes[len(runes)-4:])
}

func deployConfigResponse(site *core.Record) map[string]any {
	headers := []map[string]string{}
	for _, header := range siteDeployHeaders(site) {
		headers = append(headers, map[string]string{"name": header.Name, "value_masked": maskDeployHeaderValue(header.Value)})
	}
	return map[string]any{
		"url":        site.GetString("deploy_webhook_url"),
		"headers":    headers,
		"configured": site.GetBool("deploy_configured"),
	}
}

// ValidDeployDownloadToken reports whether token is the site's current, unexpired
// go-live download token. It stays valid until it expires rather than being
// used up, so the receiver can retry a failed download.
func ValidDeployDownloadToken(site *core.Record, token string) bool {
	stored := site.GetString("deploy_download_token")
	expires := site.GetDateTime("deploy_download_expires")
	if stored == "" || token == "" || expires.IsZero() || time.Now().After(expires.Time()) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored), []byte(token)) == 1
}

// lastPublishedAt returns when the site's published output last changed, or
// ErrSiteNotPublished. Cheaper than building the zip just to find out whether
// there is one.
func lastPublishedAt(app core.App, site *core.Record) (time.Time, error) {
	host := site.GetString("host")
	if host == "" {
		return time.Time{}, ErrSiteNotPublished
	}
	system, err := app.NewFilesystem()
	if err != nil {
		return time.Time{}, err
	}
	defer system.Close()
	files, err := system.List("sites/" + host + "/")
	if err != nil {
		return time.Time{}, err
	}
	var latest time.Time
	found := false
	for _, file := range files {
		if file.IsDir {
			continue
		}
		found = true
		if file.ModTime.After(latest) {
			latest = file.ModTime
		}
	}
	if !found {
		return time.Time{}, ErrSiteNotPublished
	}
	return latest, nil
}

// deployBaseURL is the server URL the webhook receiver downloads from. The
// configured app URL wins; without one, the URL the editor reached the server
// at is the best guess for a public address.
func deployBaseURL(e *core.RequestEvent) string {
	appURL := strings.TrimRight(e.App.Settings().Meta.AppURL, "/")
	if appURL != "" && appURL != defaultPocketBaseAppURL {
		return appURL
	}
	scheme := "http"
	if e.Request.TLS != nil || strings.EqualFold(e.Request.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + e.Request.Host
}

// findDeploySite loads the site for a deploy endpoint and checks that the
// caller is signed in. Role checks are up to the endpoint.
func findDeploySite(e *core.RequestEvent) (*core.Record, error) {
	if e.Auth == nil {
		return nil, e.UnauthorizedError("Authentication required", nil)
	}
	site, err := e.App.FindRecordById("sites", e.Request.PathValue("siteId"))
	if err != nil {
		return nil, e.NotFoundError("Site not found", err)
	}
	return site, nil
}

// RegisterDeployEndpoints registers the go-live webhook configuration
// (developers only), the Go live trigger (anyone who may publish the site) and
// keeps the deploy state fields out of reach of the record API.
func RegisterDeployEndpoints(pb *pocketbase.PocketBase) error {
	pb.OnRecordCreateRequest("sites").BindFunc(func(e *core.RecordRequestEvent) error {
		if !e.HasSuperuserAuth() {
			for _, name := range deployStateFields {
				e.Record.Set(name, nil)
			}
		}
		return e.Next()
	})
	pb.OnRecordUpdateRequest("sites").BindFunc(func(e *core.RecordRequestEvent) error {
		if !e.HasSuperuserAuth() {
			for _, name := range deployStateFields {
				e.Record.Set(name, e.Record.Original().Get(name))
			}
		}
		return e.Next()
	})

	pb.OnServe().BindFunc(func(serveEvent *core.ServeEvent) error {
		serveEvent.Router.GET("/api/primo/deploy-config/{siteId}", func(e *core.RequestEvent) error {
			site, err := findDeploySite(e)
			if err != nil {
				return err
			}
			if !canConfigureSiteDeploy(e.App, e.Auth, site.Id) {
				return e.ForbiddenError("Only developers can configure deployment", nil)
			}
			return e.JSON(http.StatusOK, deployConfigResponse(site))
		})

		serveEvent.Router.PUT("/api/primo/deploy-config/{siteId}", func(e *core.RequestEvent) error {
			site, err := findDeploySite(e)
			if err != nil {
				return err
			}
			if !canConfigureSiteDeploy(e.App, e.Auth, site.Id) {
				return e.ForbiddenError("Only developers can configure deployment", nil)
			}

			body := struct {
				URL     string `json:"url"`
				Headers []struct {
					Name string `json:"name"`
					// Omitted (or empty) keeps the stored value, so the masked
					// values from GET can be sent back unchanged.
					Value *string `json:"value"`
				} `json:"headers"`
			}{}
			if err := e.BindBody(&body); err != nil {
				return e.BadRequestError("Invalid request body", err)
			}

			webhookURL, err := normalizeDeployWebhookURL(body.URL)
			if err != nil {
				return e.BadRequestError(err.Error(), nil)
			}

			if webhookURL == "" {
				clearSiteDeploy(site)
			} else {
				if len(body.Headers) > deployMaxHeaders {
					return e.BadRequestError("Too many headers", nil)
				}
				stored := map[string]string{}
				for _, header := range siteDeployHeaders(site) {
					stored[http.CanonicalHeaderKey(header.Name)] = header.Value
				}
				headers := []deployHeader{}
				seen := map[string]bool{}
				for _, header := range body.Headers {
					name := strings.TrimSpace(header.Name)
					key := http.CanonicalHeaderKey(name)
					if !httpguts.ValidHeaderFieldName(name) {
						return e.BadRequestError("Invalid header name: "+strconv.Quote(name), nil)
					}
					if reservedDeployHeaders[key] {
						return e.BadRequestError(key+" is set by Primo and can't be configured", nil)
					}
					if seen[key] {
						return e.BadRequestError("Duplicate header: "+key, nil)
					}
					seen[key] = true

					var value string
					if header.Value != nil && *header.Value != "" {
						value = *header.Value
					} else if previous, ok := stored[key]; ok {
						value = previous
					} else {
						return e.BadRequestError("Header "+key+" needs a value", nil)
					}
					if !httpguts.ValidHeaderFieldValue(value) {
						return e.BadRequestError("Invalid value for header "+key, nil)
					}
					headers = append(headers, deployHeader{Name: name, Value: value})
				}
				site.Set("deploy_webhook_url", webhookURL)
				site.Set("deploy_webhook_headers", headers)
				site.Set("deploy_configured", true)
			}

			if err := e.App.Save(site); err != nil {
				return e.InternalServerError("Failed to save deploy configuration", err)
			}
			return e.JSON(http.StatusOK, deployConfigResponse(site))
		})

		serveEvent.Router.POST("/api/primo/deploy/{siteId}", func(e *core.RequestEvent) error {
			site, err := findDeploySite(e)
			if err != nil {
				return err
			}
			info, err := e.RequestInfo()
			if err != nil {
				return e.InternalServerError("Failed to get request info", err)
			}
			// Same check as /api/primo/generate: whoever may publish the site
			// may also send the published version live.
			if canAccess, _ := e.App.CanAccessRecord(site, info, site.Collection().UpdateRule); !canAccess {
				return e.ForbiddenError("Access denied", nil)
			}

			webhookURL := site.GetString("deploy_webhook_url")
			if webhookURL == "" {
				return e.BadRequestError("No go-live webhook is configured for this site", nil)
			}
			publishedAt, err := lastPublishedAt(e.App, site)
			if errors.Is(err, ErrSiteNotPublished) {
				return e.Error(http.StatusConflict, "This site hasn't been published yet. Publish it first, then go live.", nil)
			}
			if err != nil {
				return e.InternalServerError("Failed to read the published site", err)
			}

			// The receiver authenticates its download with this token instead
			// of a user session. Saved before the webhook fires, since the
			// receiver may start downloading before the webhook call returns.
			tokenBytes := make([]byte, 32)
			if _, err := rand.Read(tokenBytes); err != nil {
				return e.InternalServerError("Failed to create download token", err)
			}
			token := hex.EncodeToString(tokenBytes)
			expires := time.Now().Add(deployDownloadTTL).UTC()
			site.Set("deploy_download_token", token)
			site.Set("deploy_download_expires", expires)
			if err := e.App.Save(site); err != nil {
				return e.InternalServerError("Failed to save download token", err)
			}

			triggeredBy := e.Auth.Email()
			if triggeredBy == "" {
				triggeredBy = e.Auth.Id
			}
			// repository_dispatch shape. GitHub allows at most 10 top-level
			// keys in client_payload, so it stays flat and small.
			payload, err := json.Marshal(map[string]any{
				"event_type": "primo-deploy",
				"client_payload": map[string]string{
					"site_id":          site.Id,
					"site_name":        site.GetString("name"),
					"host":             site.GetString("host"),
					"public_url":       site.GetString("public_url"),
					"published_at":     publishedAt.UTC().Format(time.RFC3339),
					"download_url":     deployBaseURL(e) + "/api/primo/published/" + url.PathEscape(site.Id) + "?token=" + token,
					"download_expires": expires.Format(time.RFC3339),
					"triggered_by":     triggeredBy,
				},
			})
			if err != nil {
				return e.InternalServerError("Failed to encode webhook payload", err)
			}

			req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(payload))
			if err != nil {
				return e.InternalServerError("Invalid webhook URL", nil)
			}
			req.Header.Set("User-Agent", "primo-deploy")
			for _, header := range siteDeployHeaders(site) {
				req.Header.Set(header.Name, header.Value)
			}
			req.Header.Set("Content-Type", "application/json")

			client := &http.Client{
				Timeout: deployTimeout,
				// A redirect would resend the request (and its token) to a URL
				// nobody configured; treat it as a failure instead.
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			}
			res, sendErr := client.Do(req)
			upstreamStatus := 0
			if sendErr == nil {
				upstreamStatus = res.StatusCode
				// The body may echo the request or describe the receiver's
				// setup, so it goes to the server log, never to the browser.
				snippet, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
				res.Body.Close()
				if upstreamStatus < 200 || upstreamStatus > 299 {
					e.App.Logger().Warn("Deploy webhook failed", "site", site.Id, "status", upstreamStatus, "body", string(snippet))
				}
			} else {
				e.App.Logger().Warn("Deploy webhook unreachable", "site", site.Id, "error", sendErr.Error())
			}
			ok := sendErr == nil && upstreamStatus >= 200 && upstreamStatus <= 299

			// Reload so a site edit saved while the webhook was running isn't
			// overwritten with the copy loaded above.
			fresh, err := e.App.FindRecordById("sites", site.Id)
			if err != nil {
				return e.InternalServerError("Failed to save deploy status", err)
			}
			deployedAt := types.NowDateTime()
			if ok {
				fresh.Set("deploy_status", deployStatusOK)
				fresh.Set("deployed_at", deployedAt)
			} else {
				fresh.Set("deploy_status", deployStatusFailed)
			}
			if err := e.App.Save(fresh); err != nil {
				return e.InternalServerError("Failed to save deploy status", err)
			}

			if sendErr != nil {
				return e.Error(http.StatusBadGateway, "The deploy webhook could not be reached", nil)
			}
			if !ok {
				return e.Error(http.StatusBadGateway, "The deploy webhook responded with HTTP "+strconv.Itoa(upstreamStatus), nil)
			}
			return e.JSON(http.StatusOK, map[string]any{
				"status":      deployStatusOK,
				"deployed_at": deployedAt.String(),
			})
		})
		return serveEvent.Next()
	})
	return nil
}
