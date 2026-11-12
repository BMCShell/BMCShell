package release

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

func TestCoreReleaseRetriesIndexAndArchive(t *testing.T) {
	t.Parallel()
	const zipData = "release archive contents"
	item := indexFixture("3.0.0-dev.10", "v3.0.0-dev.10")
	item.Artifacts[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(zipData)))
	item.Artifacts[0].Bytes = int64(len(zipData))
	asset := item.Artifacts[0]
	archiveName := filepath.Base(asset.URL)
	index := indexJSON(t, item)
	manifestData, err := json.Marshal(manifest{
		Schema: 1, Product: "BMCShell", Version: item.Version, Platform: windowsAMD64,
		Artifact: archiveName, SHA256: asset.SHA256,
	})
	if err != nil {
		t.Fatal(err)
	}

	var requests []string
	var indexAttempts, zipAttempts int
	client := &http.Client{Transport: releaseTransport(func(req *http.Request) (*http.Response, error) {
		address := req.URL.String()
		requests = append(requests, address)
		if req.Method != http.MethodGet || req.Header.Get("Range") != "" {
			t.Errorf("unexpected download method or range: %s %q", req.Method, req.Header.Get("Range"))
		}

		switch address {
		case coreReleaseIndex:
			indexAttempts++
			if indexAttempts == 1 {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Header:     http.Header{"Retry-After": {"1"}},
					Body:       http.NoBody,
				}, nil
			}
			return retryReleaseResponse(strings.NewReader(index), len(index)), nil
		case asset.ManifestURL:
			return retryReleaseResponse(strings.NewReader(string(manifestData)), len(manifestData)), nil
		case asset.URL:
			zipAttempts++
			if zipAttempts == 1 {
				truncated := io.MultiReader(strings.NewReader("incomplete"), iotest.ErrReader(io.ErrUnexpectedEOF))
				return retryReleaseResponse(truncated, len(zipData)), nil
			}
			return retryReleaseResponse(strings.NewReader(zipData), len(zipData)), nil
		default:
			return nil, fmt.Errorf("unexpected release request %s", address)
		}
	})}

	var progress strings.Builder
	selected, err := SelectIndexWithProgress(t.Context(), client, "", &progress)
	if err != nil || selected.Version != item.Version {
		t.Fatalf("selected version = %q, error = %v", selected.Version, err)
	}
	stage := t.TempDir()
	archive, err := selected.Download(t.Context(), client, stage, &progress)
	if err != nil || archive != filepath.Join(stage, archiveName) {
		t.Fatalf("archive = %q, error = %v", archive, err)
	}
	data, err := os.ReadFile(archive)
	if err != nil || string(data) != zipData {
		t.Fatalf("downloaded data = %q, error = %v", data, err)
	}
	files, err := os.ReadDir(stage)
	if err != nil || len(files) != 2 {
		t.Fatalf("staging directory = %v, error = %v", files, err)
	}
	want := []string{coreReleaseIndex, coreReleaseIndex, asset.ManifestURL, asset.URL, asset.URL}
	if !slices.Equal(requests, want) {
		t.Fatalf("release requests = %v, want %v", requests, want)
	}
	if status := progress.String(); strings.Count(status, "重试") != 2 || !strings.Contains(status, "HTTP 503") {
		t.Fatalf("retry progress = %q", status)
	}
}

func retryReleaseResponse(body io.Reader, size int) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(size), Body: io.NopCloser(body)}
}
