package internal

import (
	"io"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/tools/filesystem"
	_ "github.com/primocms/primo/migrations"
)

// TestCopyIfChangedSkipsIdenticalBytes verifies the core incremental-generate
// behavior: a copy whose destination already holds identical bytes is skipped
// (the destination file is left untouched), while diverging bytes trigger a
// real copy.
func TestCopyIfChangedSkipsIdenticalBytes(t *testing.T) {
	app := newImportTestApp(t)
	defer app.ResetBootstrapState()

	system, err := app.NewFilesystem()
	if err != nil {
		t.Fatalf("open filesystem: %v", err)
	}
	defer system.Close()

	srcKey := "test/source/file.txt"
	dstKey := "test/dest/file.txt"

	if err := system.Upload([]byte("hello world"), srcKey); err != nil {
		t.Fatalf("seed source: %v", err)
	}

	// First copy: destination does not exist yet, so it must be written.
	if err := copyIfChanged(system, srcKey, dstKey); err != nil {
		t.Fatalf("initial copy: %v", err)
	}
	firstAttr, err := system.Attributes(dstKey)
	if err != nil {
		t.Fatalf("attributes after initial copy: %v", err)
	}

	// Second copy with identical source: destination should be left untouched,
	// so its ModTime must not advance.
	if err := copyIfChanged(system, srcKey, dstKey); err != nil {
		t.Fatalf("no-op copy: %v", err)
	}
	if !destinationMatchesSource(system, srcKey, dstKey) {
		t.Fatalf("expected destination to match source after no-op copy")
	}
	secondAttr, err := system.Attributes(dstKey)
	if err != nil {
		t.Fatalf("attributes after no-op copy: %v", err)
	}
	if !secondAttr.ModTime.Equal(firstAttr.ModTime) {
		t.Fatalf("no-op copy rewrote destination: modtime moved %v -> %v", firstAttr.ModTime, secondAttr.ModTime)
	}

	// Diverge the source: the next copy must rewrite the destination.
	if err := system.Upload([]byte("hello mars"), srcKey); err != nil {
		t.Fatalf("update source: %v", err)
	}
	if destinationMatchesSource(system, srcKey, dstKey) {
		t.Fatalf("expected mismatch after source changed")
	}
	if err := copyIfChanged(system, srcKey, dstKey); err != nil {
		t.Fatalf("copy after change: %v", err)
	}
	reader, err := system.GetFile(dstKey)
	if err != nil {
		t.Fatalf("read destination after change: %v", err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read bytes: %v", err)
	}
	if string(got) != "hello mars" {
		t.Fatalf("destination not updated: got %q", string(got))
	}
}

// TestGenerateSiteWritesSvelteRuntime verifies the shared Svelte runtime stored
// on the site is unpacked to sites/{host}/_svelte/{version}/ and survives the
// cleanup pass, while files of a previous runtime version are removed.
func TestGenerateSiteWritesSvelteRuntime(t *testing.T) {
	app := newImportTestApp(t)
	defer app.ResetBootstrapState()
	site := createImportTestSite(t, app)

	runtime := `{"version":"5.56.1","files":{"index.js":"export * from './chunks/runtime-abc.js'","internal/client.js":"export * from '../chunks/runtime-abc.js'","chunks/runtime-abc.js":"export const x = 1"}}`
	file, err := filesystem.NewFileFromBytes([]byte(runtime), "svelte-runtime.json")
	if err != nil {
		t.Fatal(err)
	}
	site.Set("svelte_runtime", file)
	if err := app.Save(site); err != nil {
		t.Fatal(err)
	}

	system, err := app.NewFilesystem()
	if err != nil {
		t.Fatal(err)
	}
	defer system.Close()

	base := "sites/" + site.GetString("host") + "/_svelte/"
	if err := system.Upload([]byte("stale"), base+"5.0.0/index.js"); err != nil {
		t.Fatal(err)
	}

	// Run twice: the second pass must keep the (unchanged) runtime files.
	for i := 0; i < 2; i++ {
		if err := GenerateSite(app, site); err != nil {
			t.Fatalf("generate #%d: %v", i+1, err)
		}
	}

	for path, want := range map[string]string{
		"index.js":              "export * from './chunks/runtime-abc.js'",
		"internal/client.js":    "export * from '../chunks/runtime-abc.js'",
		"chunks/runtime-abc.js": "export const x = 1",
	} {
		reader, err := system.GetReader(base + "5.56.1/" + path)
		if err != nil {
			t.Fatalf("runtime file %s missing: %v", path, err)
		}
		got, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("runtime file %s = %q, want %q", path, got, want)
		}
	}
	if exists, _ := system.Exists(base + "5.0.0/index.js"); exists {
		t.Fatalf("stale runtime version was not cleaned up")
	}
}

// TestGenerateSiteRejectsUnsafeSvelteRuntimePaths verifies runtime paths from
// the client can't escape the site's _svelte/{version}/ directory.
func TestGenerateSiteRejectsUnsafeSvelteRuntimePaths(t *testing.T) {
	for _, runtime := range []string{
		`{"version":"5.56.1","files":{"../../other/index.html":"x"}}`,
		`{"version":"5.56.1","files":{"/index.js":"x"}}`,
		`{"version":"5.56.1","files":{"chunks/../../../x.js":"x"}}`,
		`{"version":"..","files":{"index.js":"x"}}`,
		`{"version":"5.56.1/../x","files":{"index.js":"x"}}`,
	} {
		app := newImportTestApp(t)
		site := createImportTestSite(t, app)
		file, err := filesystem.NewFileFromBytes([]byte(runtime), "svelte-runtime.json")
		if err != nil {
			t.Fatal(err)
		}
		site.Set("svelte_runtime", file)
		if err := app.Save(site); err != nil {
			t.Fatal(err)
		}
		if err := GenerateSite(app, site); err == nil || !strings.Contains(err.Error(), "svelte runtime") {
			t.Fatalf("runtime %s: expected rejection, got %v", runtime, err)
		}
		app.ResetBootstrapState()
	}
}
