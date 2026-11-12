package release

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func indexFixture(version, tag string) indexedRelease {
	base := "BMCShell-" + version + "-windows-amd64"
	return indexedRelease{Version: version, Tag: tag, Artifacts: []indexedArtifact{{
		Platform: windowsAMD64, URL: coreReleaseDownload + tag + "/" + base + ".zip",
		ManifestURL: coreReleaseDownload + tag + "/" + base + ".manifest.json",
		SHA256:      strings.Repeat("a", 64), Bytes: 8,
	}}}
}

func indexJSON(t *testing.T, entries ...indexedRelease) string {
	t.Helper()
	data, err := json.Marshal(releaseIndex{Schema: 1, Product: "BMCShell", Releases: entries})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func releaseTestClient(responses map[string]string, requests *[]string) *http.Client {
	return &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		address := req.URL.String()
		*requests = append(*requests, address)
		body, found := responses[address]
		status := http.StatusNotFound
		if found {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}, nil
	})}
}

func TestSelectIndexAndDownload(t *testing.T) {
	older := indexFixture("3.0.0-dev.9", "3.0.0-dev.9")
	current := indexFixture("3.0.0-dev.10", "v3.0.0-dev.10")
	otherPlatform := indexedRelease{Version: "4.0.0", Tag: "v4.0.0", Artifacts: []indexedArtifact{{Platform: "linux-amd64"}}}
	responses := map[string]string{coreReleaseIndex: indexJSON(t, older, otherPlatform, current)}
	for _, item := range []indexedRelease{older, current} {
		asset := item.Artifacts[0]
		data, err := json.Marshal(manifest{
			Schema: 1, Product: "BMCShell", Version: item.Version, Platform: windowsAMD64,
			Artifact: filepath.Base(asset.URL), SHA256: asset.SHA256,
		})
		if err != nil {
			t.Fatal(err)
		}
		responses[asset.ManifestURL] = string(data)
		responses[asset.URL] = "zip data"
	}

	for _, tc := range []struct {
		requested string
		want      indexedRelease
	}{{"", current}, {"3.0.0-dev.9", older}} {
		t.Run(tc.want.Version, func(t *testing.T) {
			var requests []string
			client := releaseTestClient(responses, &requests)
			ctx := context.Background()
			selected, err := SelectIndex(ctx, client, tc.requested)
			if err != nil || selected.Version != tc.want.Version {
				t.Fatalf("selected %s: %v", selected.Version, err)
			}
			if len(requests) != 1 || requests[0] != coreReleaseIndex {
				t.Fatalf("selection requests = %v", requests)
			}
			var progress strings.Builder
			archive, err := selected.Download(ctx, client, t.TempDir(), &progress)
			asset := tc.want.Artifacts[0]
			if err != nil || filepath.Base(archive) != filepath.Base(asset.URL) {
				t.Fatalf("archive = %q: %v", archive, err)
			}
			if !strings.Contains(progress.String(), "正在下载发布校验清单") ||
				!strings.Contains(progress.String(), "正在下载框架发布包 "+filepath.Base(asset.URL)) {
				t.Fatalf("missing download stages: %q", progress.String())
			}
			if len(requests) != 3 || requests[1] != asset.ManifestURL || requests[2] != asset.URL {
				t.Fatalf("download requests = %v", requests)
			}
		})
	}
}

func TestSelectIndexRejectsInvalidMetadata(t *testing.T) {
	item := indexFixture("3.0.0-dev.10", "v3.0.0-dev.10")
	item.Artifacts[0].ManifestURL = "https://example.test/manifest.json"
	for _, tc := range []struct{ name, body, version, want string }{
		{"missing index", "", "", "HTTP 404"},
		{"invalid schema", `{"schema":2,"product":"BMCShell","releases":[]}`, "", "索引无效"},
		{"missing version", indexJSON(t, item), "3.0.0-dev.9", "没有版本 3.0.0-dev.9"},
		{"off-repository asset", indexJSON(t, item), "", "发布包信息无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responses := make(map[string]string)
			if tc.body != "" {
				responses[coreReleaseIndex] = tc.body
			}
			var requests []string
			_, err := SelectIndex(context.Background(), releaseTestClient(responses, &requests), tc.version)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(requests) != 1 {
				t.Fatalf("err = %v, requests = %v", err, requests)
			}
		})
	}
}

func TestIndexDownloadChecksManifestDigest(t *testing.T) {
	item := indexFixture("3.0.0-dev.10", "v3.0.0-dev.10")
	asset := item.Artifacts[0]
	sidecar, err := json.Marshal(manifest{
		Schema: 1, Product: "BMCShell", Version: item.Version, Platform: windowsAMD64,
		Artifact: filepath.Base(asset.URL), SHA256: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	responses := map[string]string{coreReleaseIndex: indexJSON(t, item), asset.ManifestURL: string(sidecar)}
	var requests []string
	client := releaseTestClient(responses, &requests)
	ctx := context.Background()
	selected, err := SelectIndex(ctx, client, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selected.Download(ctx, client, t.TempDir(), io.Discard); err == nil || !strings.Contains(err.Error(), "SHA-256") || len(requests) != 2 {
		t.Fatalf("err = %v, requests = %v", err, requests)
	}
}

func TestReleaseTimeoutIdentifiesStage(t *testing.T) {
	item := indexFixture("3.0.0-dev.10", "v3.0.0-dev.10")
	asset := item.Artifacts[0]
	sidecar, err := json.Marshal(manifest{
		Schema: 1, Product: "BMCShell", Version: item.Version, Platform: windowsAMD64,
		Artifact: filepath.Base(asset.URL), SHA256: asset.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, url, want string }{
		{"before index response", coreReleaseIndex, "读取框架发行版索引失败"},
		{"while reading manifest", asset.ManifestURL, "下载发布校验清单失败"},
		{"while reading ZIP", asset.URL, "下载框架发布包失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodies := map[string]string{coreReleaseIndex: indexJSON(t, item), asset.ManifestURL: string(sidecar)}
			client := &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
				address := req.URL.String()
				if address == tc.url {
					if address == coreReleaseIndex {
						return nil, context.DeadlineExceeded
					}
					return &http.Response{
						StatusCode: http.StatusOK, ContentLength: -1,
						Body: io.NopCloser(iotest.ErrReader(context.DeadlineExceeded)),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(bodies[address])),
				}, nil
			})}
			ctx := context.Background()
			selected, err := SelectIndex(ctx, client, "")
			if err == nil {
				_, err = selected.Download(ctx, client, t.TempDir(), io.Discard)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "网络请求超时") {
				t.Fatalf("timeout = %v, want stage %q and a localized timeout", err, tc.want)
			}
		})
	}
}
