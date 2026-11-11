package release

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestGitHubReleaseAssetPair(t *testing.T) {
	responses := make(map[string]string)
	makeRelease := func(tag string, complete bool) githubRelease {
		version := strings.TrimPrefix(tag, "v")
		base := "BMCShell-" + version + "-windows-amd64"
		prefix := "https://github.com/BMCShell/BMCShell/releases/download/" + tag + "/"
		archive := githubAsset{Name: base + ".zip", URL: prefix + base + ".zip"}
		sidecar := githubAsset{Name: base + ".manifest.json", URL: prefix + base + ".manifest.json"}
		item := githubRelease{Tag: tag, Assets: []githubAsset{archive}}
		if complete {
			item.Assets = append(item.Assets, sidecar)
		}
		responses[archive.URL] = "zip data"
		data, err := json.Marshal(manifest{
			Schema: 1, Product: "BMCShell", Version: version, Platform: "windows-amd64",
			Artifact: archive.Name, SHA256: strings.Repeat("0", 64),
		})
		if err != nil {
			t.Fatal(err)
		}
		responses[sidecar.URL] = string(data)
		return item
	}
	current := makeRelease("v3.0.0", true)
	older := makeRelease("2.0.0", true)
	missingSidecar := makeRelease("v4.0.0", false)
	list, err := json.Marshal([]githubRelease{missingSidecar, older, current})
	if err != nil {
		t.Fatal(err)
	}
	responses[githubReleases+"?per_page=100"] = string(list)
	tag, err := json.Marshal(older)
	if err != nil {
		t.Fatal(err)
	}
	responses[githubReleases+"/tags/2.0.0"] = string(tag)

	var assetRequests int
	client := &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Hostname() == "github.com" {
			assetRequests++
		}
		body, found := responses[req.URL.String()]
		status := http.StatusNotFound
		if found {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
	})}
	for _, tc := range []struct{ requested, want string }{{"", "3.0.0"}, {"2.0.0", "2.0.0"}} {
		t.Run(tc.want, func(t *testing.T) {
			assetRequests = 0
			ctx := context.Background()
			selected, err := SelectGitHub(ctx, client, tc.requested)
			if err != nil || selected.Version != tc.want {
				t.Fatalf("selected %s: %v", selected.Version, err)
			}
			if assetRequests != 0 {
				t.Fatalf("select downloaded %d assets", assetRequests)
			}
			archive, err := selected.Download(ctx, client, t.TempDir())
			if err != nil || assetRequests != 2 {
				t.Fatalf("download requested %d assets: %v", assetRequests, err)
			}
			if filepath.Base(archive) != "BMCShell-"+tc.want+"-windows-amd64.zip" {
				t.Fatalf("unexpected archive: %s", archive)
			}
			if _, err := os.Stat(strings.TrimSuffix(archive, ".zip") + ".manifest.json"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
