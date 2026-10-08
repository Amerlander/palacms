package internal

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"mime"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

// ErrSiteNotPublished is returned by BuildPublishedSiteZip when the site has no
// published files yet (sites/{host}/ is empty), so callers can tell "publish
// first" apart from a real failure.
var ErrSiteNotPublished = errors.New("site has not been published yet")

// publishedTextExtensions are the published files that may contain absolute
// URLs pointing at the Primo host. Everything else (images, fonts, uploads) is
// copied byte for byte.
var publishedTextExtensions = map[string]bool{
	".html":        true,
	".xml":         true,
	".txt":         true,
	".css":         true,
	".js":          true,
	".json":        true,
	".webmanifest": true,
}

// NormalizePublicURL validates a site's public_url and returns its canonical
// form: empty, or an absolute http(s) URL without query, fragment or trailing
// slash. The value is spliced into published files in place of the Primo host,
// so anything that isn't a plain base URL would produce broken links.
func NormalizePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	invalid := errors.New("Public URL must be an absolute http(s) URL without query or fragment, e.g. https://www.example.com")
	if strings.ContainsAny(raw, "?# ") {
		return "", invalid
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", invalid
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", invalid
	}
	return scheme + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// BuildPublishedSiteZip zips the site's published output (everything under
// sites/{host}/, with paths relative to that folder) so it can be deployed to
// any static host, leaving the Primo server as a preview host. Reads only what
// the last publish wrote — it never renders — so the zip is exactly what Primo
// serves for the site.
//
// When the site has a public_url, absolute links to the Primo host in text
// files are rewritten to it (see rewritePublishedHost), and a minimal
// robots.txt pointing at the sitemap is added if the output has none, since
// crawlers of the deployed copy would otherwise not find the sitemap.
func BuildPublishedSiteZip(app core.App, site *core.Record) ([]byte, error) {
	host := site.GetString("host")
	if host == "" {
		return nil, ErrSiteNotPublished
	}
	publicURL := site.GetString("public_url")

	system, err := app.NewFilesystem()
	if err != nil {
		return nil, err
	}
	defer system.Close()

	prefix := "sites/" + host + "/"
	files, err := system.List(prefix)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(files))
	for _, file := range files {
		if !file.IsDir {
			keys = append(keys, file.Key)
		}
	}
	if len(keys) == 0 {
		return nil, ErrSiteNotPublished
	}
	// Stable entry order, so two downloads of the same publish are identical.
	sort.Strings(keys)

	var rewrite func([]byte) []byte
	if publicURL != "" {
		rewrite = rewritePublishedHost(host, publicURL)
	}

	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	hasRobots, hasSitemap := false, false
	for _, key := range keys {
		name := strings.TrimPrefix(key, prefix)
		switch name {
		case "robots.txt":
			hasRobots = true
		case "sitemap.xml":
			hasSitemap = true
		}

		reader, err := system.GetReader(key)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			return nil, err
		}

		if rewrite != nil && publishedTextExtensions[strings.ToLower(path.Ext(name))] {
			data = rewrite(data)
		}
		if err := writeFileToZip(zw, name, data); err != nil {
			return nil, err
		}
	}

	if publicURL != "" && hasSitemap && !hasRobots {
		robots := "User-agent: *\nAllow: /\nSitemap: " + publicURL + "/sitemap.xml\n"
		if err := writeFileToZip(zw, "robots.txt", []byte(robots)); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// rewritePublishedHost returns a function that replaces absolute links to the
// Primo host (https://host, http://host and protocol-relative //host) with
// publicURL. A match must end at the host: "//example.com" must not rewrite
// the start of "//example.com.cdn.net" or "//example.community".
//
// Limitation: only absolute URLs are rewritten. If publicURL has a path prefix
// (https://example.com/docs), sitemap locs and absolute links come out right,
// but root-relative references in the pages (/about, /_symbols/x.js) still
// point at the domain root, so such a deployment needs the files served from
// the root of their host to work fully.
func rewritePublishedHost(host, publicURL string) func([]byte) []byte {
	pattern := regexp.MustCompile(`(?:https?:)?//` + regexp.QuoteMeta(host) + `[A-Za-z0-9.-]*`)
	return func(data []byte) []byte {
		return pattern.ReplaceAllFunc(data, func(match []byte) []byte {
			rest := match[bytes.Index(match, []byte("//"))+2+len(host):]
			// Trailing dots are sentence punctuation ("see https://host."),
			// anything else means the match is a longer hostname.
			if len(bytes.Trim(rest, ".")) > 0 {
				return match
			}
			return append([]byte(publicURL), rest...)
		})
	}
}

// canConfigureSiteDeploy reports whether auth may change how a site is
// deployed outside Primo (the dashboard's Publishing settings), such as its
// public_url, which decides where the deployed copy claims to live (sitemap,
// links). Like head/foot code that's developer configuration, so it's limited
// to developers. Mirrors the editor's notion of a developer (src/lib/pocketbase/
// user.ts): the server role wins, and only users without one fall back to
// their site role assignment.
func canConfigureSiteDeploy(app core.App, auth *core.Record, siteId string) bool {
	if auth == nil {
		return false
	}
	if auth.IsSuperuser() {
		return true
	}
	switch auth.GetString("serverRole") {
	case "developer":
		return true
	case "":
	default:
		return false
	}
	if siteId == "" {
		return false
	}
	count, err := app.CountRecords("site_role_assignments", dbx.HashExp{"site": siteId, "user": auth.Id, "role": "developer"})
	return err == nil && count > 0
}

// RegisterPublicURLValidation normalizes sites.public_url on every API write
// and rejects changes to it by non-developers. The sites update rule only asks
// whether a user belongs to the site, not in which role, so the role check has
// to live in a request hook.
func RegisterPublicURLValidation(pb *pocketbase.PocketBase) error {
	check := func(e *core.RecordRequestEvent, previous string) error {
		normalized, err := NormalizePublicURL(e.Record.GetString("public_url"))
		if err != nil {
			return e.BadRequestError(err.Error(), nil)
		}
		e.Record.Set("public_url", normalized)
		if normalized != previous && !canConfigureSiteDeploy(e.App, e.Auth, e.Record.Id) {
			return e.ForbiddenError("Only developers can change the public URL", nil)
		}
		return e.Next()
	}
	pb.OnRecordCreateRequest("sites").BindFunc(func(e *core.RecordRequestEvent) error {
		return check(e, "")
	})
	pb.OnRecordUpdateRequest("sites").BindFunc(func(e *core.RecordRequestEvent) error {
		return check(e, e.Record.Original().GetString("public_url"))
	})
	return nil
}

// RegisterPublishedEndpoint serves BuildPublishedSiteZip as a download. Access
// mirrors the export endpoint, but checks the update rule like
// /api/primo/generate: the zip is the publish output, so it's available to
// whoever may publish the site.
func RegisterPublishedEndpoint(pb *pocketbase.PocketBase) error {
	pb.OnServe().BindFunc(func(serveEvent *core.ServeEvent) error {
		serveEvent.Router.GET("/api/primo/published/{siteId}", func(e *core.RequestEvent) error {
			siteId := e.Request.PathValue("siteId")
			if siteId == "" {
				return e.BadRequestError("Missing site ID", nil)
			}

			// Allow unauthenticated access from localhost (for primo dev)
			isLocal := IsLocalhost(e)

			if e.Auth == nil && !isLocal {
				return e.UnauthorizedError("Authentication required", nil)
			}

			site, err := pb.FindRecordById("sites", siteId)
			if err != nil {
				return e.NotFoundError("Site not found", err)
			}

			if !isLocal {
				info, err := e.RequestInfo()
				if err != nil {
					return e.InternalServerError("Failed to get request info", err)
				}
				canAccess, _ := e.App.CanAccessRecord(site, info, site.Collection().UpdateRule)
				if !canAccess {
					return e.ForbiddenError("Access denied", nil)
				}
			}

			zipData, err := BuildPublishedSiteZip(pb, site)
			if errors.Is(err, ErrSiteNotPublished) {
				return e.NotFoundError("This site hasn't been published yet. Publish it first, then download it.", nil)
			}
			if err != nil {
				return e.InternalServerError("Download failed: "+err.Error(), err)
			}

			// Same header-safe filename handling as the export endpoint.
			filename := asciiHeaderFilename(sanitizeFilename(site.GetString("name"))) + "-published.zip"
			disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
			if disposition == "" {
				disposition = "attachment"
			}
			e.Response.Header().Set("Cache-Control", "no-store")
			e.Response.Header().Set("Content-Type", "application/zip")
			e.Response.Header().Set("Content-Disposition", disposition)
			e.Response.Write(zipData)
			return nil
		})
		return serveEvent.Next()
	})
	return nil
}
