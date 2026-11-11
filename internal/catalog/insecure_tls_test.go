package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"bmcshell/internal/packages"
)

func TestPrivateSourceTLSOptIn(t *testing.T) {
	const (
		sourceOrigin = "https://10.23.45.67:8443"
		otherPort    = "https://10.23.45.67:9443"
		otherPrivate = "https://10.23.45.68:8443"
		publicHost   = "https://packages.example.com"
	)
	manifest := packages.Manifest{
		Schema: 1, CoreAPI: 1, ID: "9045a80b-d55f-48d0-849b-c19bb02b3056",
		Name: "bmcshell-book-fixture", Type: "book", Version: "1.0.0",
	}
	archive := packageArchive(t, manifest)
	tests := []struct {
		name         string
		insecure     bool
		indexPath    string
		artifactURL  string
		trustFixture bool
		failRefresh  bool
		failZIP      bool
	}{
		{name: "strict by default", indexPath: "/index", artifactURL: "asset", failRefresh: true},
		{name: "same origin redirects", insecure: true, indexPath: "/local-index", artifactURL: "local-asset"},
		{name: "index redirect to another port", insecure: true, indexPath: "/foreign-index", artifactURL: "asset", failRefresh: true},
		{name: "asset at another private IP", insecure: true, indexPath: "/index", artifactURL: otherPrivate + "/asset", failZIP: true},
		{name: "asset redirect to a domain", insecure: true, indexPath: "/index", artifactURL: "foreign-asset", failZIP: true},
		{name: "trusted asset redirect to a domain", insecure: true, indexPath: "/index", artifactURL: "foreign-asset", trustFixture: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index, err := json.Marshal(indexFile{
				Schema: 1,
				Packages: []Entry{{
					ID: manifest.ID, Name: manifest.Name, Type: manifest.Type, Version: manifest.Version,
					CoreAPI: manifest.CoreAPI, URL: tt.artifactURL, SHA256: fmt.Sprintf("%x", sha256.Sum256(archive)),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/index":
					_, _ = w.Write(index)
				case "/asset":
					_, _ = w.Write(archive)
				case "/local-index":
					http.Redirect(w, req, "/index", http.StatusFound)
				case "/foreign-index":
					http.Redirect(w, req, otherPort+"/index", http.StatusFound)
				case "/local-asset":
					http.Redirect(w, req, "/asset", http.StatusFound)
				case "/foreign-asset":
					http.Redirect(w, req, publicHost+"/asset", http.StatusFound)
				default:
					http.NotFound(w, req)
				}
			}))
			defer server.Close()

			store := Open(t.TempDir())
			transport := store.client.Transport.(*http.Transport)
			if tt.trustFixture {
				transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			}
			dialer := new(net.Dialer)
			// Exercise private-IP and domain URLs without connecting outside the fixture.
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, server.Listener.Addr().String())
			}
			t.Cleanup(transport.CloseIdleConnections)

			source := Source{Name: "internal", URL: sourceOrigin + tt.indexPath, InsecureTLS: tt.insecure}
			if err := store.SetSource(source); err != nil {
				t.Fatal(err)
			}
			sources, err := store.Sources()
			if err != nil || len(sources) != 1 || sources[0] != source {
				t.Fatalf("stored source: %+v, %v", sources, err)
			}
			results, err := store.Refresh(t.Context(), source.Name)
			if (err != nil) != tt.failRefresh {
				t.Fatalf("refresh: %+v, %v", results, err)
			}
			if tt.failRefresh {
				return
			}
			entries, err := store.Entries()
			if err != nil || len(entries) != 1 {
				t.Fatalf("cached entries: %+v, %v", entries, err)
			}
			path, err := store.Download(t.Context(), entries[0], t.TempDir())
			if (err != nil) != tt.failZIP || !tt.failZIP && path == "" {
				t.Fatalf("download: %q, %v", path, err)
			}
			if tt.failZIP || tt.trustFixture {
				return
			}

			previousEntry := entries[0]
			source.InsecureTLS = false
			if err := store.SetSource(source); err != nil {
				t.Fatal(err)
			}
			entries, err = store.Entries()
			if err != nil || len(entries) != 0 {
				t.Fatalf("disabling insecure TLS retained the insecure cache: %+v, %v", entries, err)
			}
			if _, err := store.Refresh(t.Context(), source.Name); err == nil {
				t.Fatal("refresh accepted the untrusted certificate after disabling insecure TLS")
			}
			if _, err := store.Download(t.Context(), previousEntry, t.TempDir()); err == nil {
				t.Fatal("previously resolved entry used insecure TLS after disabling it")
			}
		})
	}
}

func TestInsecureTLSRejectsNonPrivateOrigins(t *testing.T) {
	store := Open(t.TempDir())
	for _, rawURL := range []string{
		"https://packages.example.com/index.json",
		"https://8.8.8.8/index.json",
		"https://127.0.0.1/index.json",
		"http://127.0.0.1/index.json",
	} {
		t.Run(rawURL, func(t *testing.T) {
			source := Source{Name: "internal", URL: rawURL, InsecureTLS: true}
			if err := store.SetSource(source); err == nil {
				t.Fatalf("accepted insecure TLS for %s", rawURL)
			}
		})
	}
}
