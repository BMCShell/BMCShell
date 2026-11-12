package catalog

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bmcshell/internal/packages"
)

func TestRefreshAndDownload(t *testing.T) {
	manifest := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "9045a80b-d55f-48d0-849b-c19bb02b3056",
		Name: "bmcshell-book-fixture", Type: "book", Version: "1.0.0",
		Platforms: []string{"any"},
	}
	artifact := packageArchive(t, manifest)
	entry := Entry{
		ID: manifest.ID, Name: manifest.Name, Type: manifest.Type, Version: manifest.Version,
		CoreAPI: 1, Platforms: manifest.Platforms, URL: "artifact?private=fixture",
		SHA256: fmt.Sprintf("%x", sha256.Sum256(artifact)),
	}
	index, err := json.Marshal(indexFile{Schema: 1, Packages: []Entry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	var invalid atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/stable/index.json":
			if invalid.Load() {
				_, _ = w.Write([]byte(`{"schema":2}`))
			} else {
				_, _ = w.Write(index)
			}
		case "/stable/artifact":
			http.Redirect(w, req, "/asset", http.StatusFound)
		case "/asset":
			if req.Referer() != "" {
				t.Error("artifact redirect leaked the URL in Referer")
			}
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	store := Open(t.TempDir())
	source := Source{Name: "official", Official: true, URL: server.URL + "/stable/index.json"}
	if err := store.SetSource(source); err != nil {
		t.Fatal(err)
	}
	results, err := store.Refresh(context.Background(), "")
	if err != nil || len(results) != 1 || results[0].Source != source.Name || results[0].Count != 1 {
		t.Fatalf("refresh results: %+v, %v", results, err)
	}
	invalid.Store(true)
	if _, err := store.Refresh(context.Background(), source.Name); err == nil {
		t.Fatal("invalid refresh succeeded")
	}
	cached, err := store.Entries()
	if err != nil || len(cached) != 1 || cached[0].Source != source.Name ||
		cached[0].URL != server.URL+"/stable/artifact?private=fixture" {
		t.Fatalf("retained cached entries: %+v, %v", cached, err)
	}
	stage := t.TempDir()
	path, err := Download(context.Background(), cached[0], stage)
	if err != nil || !strings.HasSuffix(path, ".zip") {
		t.Fatalf("download: %q, %v", path, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	wrong := cached[0]
	wrong.SHA256 = strings.Repeat("0", 64)
	if _, err := Download(context.Background(), wrong, stage); err == nil || !strings.Contains(err.Error(), "SHA-256 校验失败") {
		t.Fatalf("checksum error: %v", err)
	}
	wrong = cached[0]
	wrong.Dependencies = map[string]string{"6339f070-59cd-43dc-a9eb-7a0f9e70c392": "1.0.0"}
	if _, err := Download(context.Background(), wrong, stage); err == nil || !strings.Contains(err.Error(), "ZIP 清单与软件源索引不一致") {
		t.Fatalf("manifest metadata error: %v", err)
	}
	files, err := os.ReadDir(stage)
	if err != nil || len(files) != 0 {
		t.Fatalf("failed downloads left files: %v, %v", files, err)
	}
	source.URL += "?replacement=1"
	if err := store.SetSource(source); err != nil {
		t.Fatal(err)
	}
	if entries, err := store.Entries(); err != nil || len(entries) != 0 {
		t.Fatalf("replaced source used an old cache: %+v, %v", entries, err)
	}
	if err := store.RemoveSource(source.Name); err != nil {
		t.Fatal(err)
	}
	if sources, err := store.Sources(); err != nil || len(sources) != 0 {
		t.Fatalf("removed source still present: %+v, %v", sources, err)
	}
}

func TestNetworkTimeoutPreservesErrorAndCleansUp(t *testing.T) {
	const privateQuery = "token=private-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/artifact.zip" {
			_, _ = w.Write([]byte("partial ZIP"))
			w.(http.Flusher).Flush()
		}
		<-req.Context().Done()
	}))
	defer server.Close()

	store := Open(t.TempDir())
	store.client.Timeout = 100 * time.Millisecond
	source := Source{Name: "official", Official: true, URL: server.URL + "/index.json?" + privateQuery}
	if err := store.SetSource(source); err != nil {
		t.Fatal(err)
	}
	checkTimeout := func(err error) {
		t.Helper()
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "超时") || strings.Contains(err.Error(), privateQuery) {
			t.Fatalf("timeout error = %v", err)
		}
	}
	_, err := store.Refresh(t.Context(), "")
	checkTimeout(err)
	entries, err := store.CachedEntries(source)
	if err != nil || len(entries) != 0 {
		t.Fatalf("timeout populated the cache: %v, %v", entries, err)
	}

	stage := t.TempDir()
	entry := Entry{
		ID: "9045a80b-d55f-48d0-849b-c19bb02b3056", Name: "bmcshell-book-fixture",
		Type: "book", Version: "1.0.0", CoreAPI: 1, Platforms: []string{"any"},
		URL: server.URL + "/artifact.zip?" + privateQuery, SHA256: strings.Repeat("0", 64),
	}
	_, err = store.Download(t.Context(), entry, stage)
	checkTimeout(err)
	files, err := os.ReadDir(stage)
	if err != nil || len(files) != 0 {
		t.Fatalf("timeout left files in the staging directory: %v, %v", files, err)
	}
}

func TestIndexArtifactVariants(t *testing.T) {
	entry := Entry{
		ID: "9045a80b-d55f-48d0-849b-c19bb02b3056", Name: "bmcshell-book-fixture",
		Type: "book", Version: "1.0.0", CoreAPI: 1,
		Platforms: []string{runtime.GOOS + "-" + runtime.GOARCH},
		URL:       "https://other.example.org/v1.zip", SHA256: strings.Repeat("0", 64),
	}
	variant := entry
	variant.Platforms = []string{"fixture-os"}
	source := Source{Name: "custom", URL: "https://example.org/catalog.json"}
	check := func(entries []Entry, wantError bool) {
		t.Helper()
		data, err := json.Marshal(indexFile{Schema: 1, Packages: entries})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseIndex(source, data); (err != nil) != wantError {
			t.Fatalf("parseIndex(%+v): %v", entries, err)
		}
	}
	check([]Entry{entry, variant}, false)
	check([]Entry{entry, entry}, true)
	variant.Platforms = nil
	check([]Entry{entry, variant}, true)
	variant.URL = "http://127.0.0.1/unsafe"
	check([]Entry{variant}, true)
}

func TestCompareVersion(t *testing.T) {
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.2",
		"1.0.10", "9999999999999999999999999.0.0",
	}
	for i := 1; i < len(ordered); i++ {
		a, b := ordered[i-1], ordered[i]
		if CompareVersion(a, b) >= 0 || CompareVersion(b, a) <= 0 {
			t.Errorf("expected %s < %s", a, b)
		}
	}
	if CompareVersion("1.2.3+old", "1.2.3+new") != 0 {
		t.Fatal("build metadata affected precedence")
	}
}

func packageArchive(t *testing.T, manifest packages.Manifest) []byte {
	t.Helper()
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	file, err := archive.Create(packages.ManifestFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}
