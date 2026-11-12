package catalog

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"bmcshell/internal/packages"
)

func TestRefreshRetriesStatusAndTruncatedBody(t *testing.T) {
	t.Parallel()
	index := retryIndex(t, "2.0.0")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			writeTruncated(w, index)
		default:
			_, _ = w.Write(index)
		}
	}))
	defer server.Close()

	store, source := retrySource(t, server.URL)
	results, err := store.Refresh(t.Context(), source.Name)
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Count != 1 {
		t.Fatalf("refresh results = %+v, error = %v", results, err)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("index requests = %d, want 3", got)
	}
	checkRetryCache(t, store, source, "2.0.0")
}

func TestRefreshRetryFailureKeepsOldIndex(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		status     int
		attempts   int32
		wantDetail string
	}{
		{"transient status exhausted", http.StatusBadGateway, 3, "502"},
		{"permanent status", http.StatusNotFound, 1, "404"},
		{"invalid index", http.StatusOK, 1, "索引无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			index := retryIndex(t, "1.0.0")
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if requests.Add(1) == 1 {
					_, _ = w.Write(index)
					return
				}
				if tc.status == http.StatusOK {
					_, _ = w.Write([]byte(`{"schema":2}`))
					return
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()

			store, source := retrySource(t, server.URL)
			if _, err := store.Refresh(t.Context(), source.Name); err != nil {
				t.Fatal(err)
			}
			results, err := store.Refresh(t.Context(), source.Name)
			if err == nil || len(results) != 1 || results[0].Err == nil ||
				!strings.Contains(err.Error(), tc.wantDetail) || strings.Contains(err.Error(), "retry-fixture-private") {
				t.Fatalf("failed refresh results = %+v, error = %v", results, err)
			}
			if got := requests.Load() - 1; got != tc.attempts {
				t.Fatalf("index requests = %d, want %d", got, tc.attempts)
			}
			checkRetryCache(t, store, source, "1.0.0")
		})
	}
}

func TestDownloadRetriesTruncatedArchive(t *testing.T) {
	t.Parallel()
	manifest := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "55f68a70-0e2d-45c0-abda-d31e408d69af",
		Name: "bmcshell-book-retry", Type: "book", Version: "1.0.0",
	}
	archive := packageArchive(t, manifest)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writeTruncated(w, archive)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	entry := Entry{
		ID: manifest.ID, Name: manifest.Name, Type: manifest.Type, Version: manifest.Version,
		CoreAPI: manifest.CoreAPI, URL: server.URL + "/package.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(archive)),
	}
	stage := t.TempDir()
	store := Open(t.TempDir())
	path, err := store.Download(t.Context(), entry, stage)
	if err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("archive requests = %d, want 2", got)
	}
	files, err := os.ReadDir(stage)
	if err != nil || len(files) != 1 || files[0].Name() != filepath.Base(path) {
		t.Fatalf("download left unexpected files in staging: %v, %v", files, err)
	}
}

func retryIndex(t *testing.T, version string) []byte {
	t.Helper()
	entry := Entry{
		ID: "55f68a70-0e2d-45c0-abda-d31e408d69af", Name: "bmcshell-book-retry",
		Type: "book", Version: version, CoreAPI: 1,
		URL: "package.zip", SHA256: strings.Repeat("0", 64),
	}
	data, err := json.Marshal(indexFile{Schema: 1, Packages: []Entry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func retrySource(t *testing.T, baseURL string) (*Store, Source) {
	t.Helper()
	store := Open(t.TempDir())
	source := Source{Name: "official", Official: true, URL: baseURL + "/index.json?token=retry-fixture-private"}
	if err := store.SetSource(source); err != nil {
		t.Fatal(err)
	}
	return store, source
}

func checkRetryCache(t *testing.T, store *Store, source Source, version string) {
	t.Helper()
	entries, err := store.CachedEntries(source)
	if err != nil || len(entries) != 1 || entries[0].Version != version {
		t.Fatalf("cached entries = %+v, error = %v, want version %s", entries, err, version)
	}
}

func writeTruncated(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data[:len(data)/2])
}
