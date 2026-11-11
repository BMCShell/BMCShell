package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"bmcshell/internal/catalog"
	"bmcshell/internal/packages"
)

const githubReleases = "https://api.github.com/repos/BMCShell/BMCShell/releases"

type githubRelease struct {
	Tag    string        `json:"tag_name"`
	Draft  bool          `json:"draft"`
	Assets []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// GitHubRelease is a selected pair of release assets for Windows x64.
type GitHubRelease struct {
	Version                              string
	archiveName, archiveURL, manifestURL string
}

func githubClient(client *http.Client) *http.Client {
	copy := *client
	copy.CheckRedirect = func(req *http.Request, previous []*http.Request) error {
		if len(previous) >= 10 || !githubReleaseURL(req.URL) {
			return errors.New("unsafe GitHub release redirect")
		}
		req.Header.Del("Referer")
		return nil
	}
	return &copy
}

// SelectGitHub reads release metadata without downloading either asset.
func SelectGitHub(ctx context.Context, client *http.Client, version string) (GitHubRelease, error) {
	releases, err := fetchGitHubReleases(ctx, githubClient(client), version)
	if err != nil {
		return GitHubRelease{}, err
	}
	var selected GitHubRelease
	for _, item := range releases {
		assets, ok := assetsForRelease(item)
		if !ok || version != "" && assets.Version != strings.TrimPrefix(version, "v") {
			continue
		}
		if selected.Version == "" || catalog.CompareVersion(assets.Version, selected.Version) > 0 {
			selected = assets
		}
	}
	if selected.Version == "" {
		return GitHubRelease{}, errors.New("no matching Windows x64 BMCShell release with both ZIP and checksum manifest was found")
	}
	return selected, nil
}

// Download retrieves the selected assets into a private staging directory.
func (r GitHubRelease) Download(ctx context.Context, client *http.Client, directory string) (string, error) {
	client = githubClient(client)
	archive := filepath.Join(directory, r.archiveName)
	manifestPath := strings.TrimSuffix(archive, ".zip") + ".manifest.json"
	if err := saveGitHubAsset(ctx, client, r.manifestURL, manifestPath, 16<<10); err != nil {
		return "", err
	}
	manifest, err := readManifest(archive)
	if err != nil {
		return "", err
	}
	if manifest.Version != r.Version {
		return "", errors.New("release checksum manifest version differs from the GitHub tag")
	}
	if err := saveGitHubAsset(ctx, client, r.archiveURL, archive, maxArchiveBytes); err != nil {
		return "", err
	}
	return archive, nil
}

func fetchGitHubReleases(ctx context.Context, client *http.Client, version string) ([]githubRelease, error) {
	if version == "" {
		response, err := githubGet(ctx, client, githubReleases+"?per_page=100")
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GitHub Releases returned HTTP %d", response.StatusCode)
		}
		var releases []githubRelease
		if err := decodeGitHubReleases(response.Body, &releases); err != nil {
			return nil, err
		}
		return releases, nil
	}
	version = strings.TrimPrefix(version, "v")
	if err := packages.ValidateVersion(version); err != nil {
		return nil, err
	}
	for _, tag := range []string{"v" + version, version} {
		item, found, err := fetchGitHubTag(ctx, client, tag)
		if err != nil {
			return nil, err
		}
		if found {
			if _, compatible := assetsForRelease(item); compatible {
				return []githubRelease{item}, nil
			}
		}
	}
	return nil, nil
}

func fetchGitHubTag(ctx context.Context, client *http.Client, tag string) (githubRelease, bool, error) {
	response, err := githubGet(ctx, client, githubReleases+"/tags/"+url.PathEscape(tag))
	if err != nil {
		return githubRelease{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return githubRelease{}, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return githubRelease{}, false, fmt.Errorf("GitHub Releases returned HTTP %d", response.StatusCode)
	}
	var item githubRelease
	err = decodeGitHubReleases(response.Body, &item)
	return item, err == nil, err
}

func decodeGitHubReleases(body io.Reader, target any) error {
	const maxMetadataBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(body, maxMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxMetadataBytes || json.Unmarshal(data, target) != nil {
		return errors.New("invalid or oversized GitHub release metadata")
	}
	return nil
}

func assetsForRelease(item githubRelease) (GitHubRelease, bool) {
	version := strings.TrimPrefix(item.Tag, "v")
	if item.Draft || !validVersion(version) {
		return GitHubRelease{}, false
	}
	base := "BMCShell-" + version + "-windows-amd64"
	result := GitHubRelease{Version: version, archiveName: base + ".zip"}
	for _, asset := range item.Assets {
		var destination *string
		switch asset.Name {
		case result.archiveName:
			destination = &result.archiveURL
		case base + ".manifest.json":
			destination = &result.manifestURL
		default:
			continue
		}
		parsed, err := url.Parse(asset.URL)
		if *destination != "" || err != nil || !githubReleaseURL(parsed) || parsed.Hostname() != "github.com" ||
			!strings.EqualFold(strings.TrimPrefix(parsed.Path, "/"), "BMCShell/BMCShell/releases/download/"+item.Tag+"/"+asset.Name) {
			return GitHubRelease{}, false
		}
		*destination = asset.URL
	}
	return result, result.archiveURL != "" && result.manifestURL != ""
}

func githubReleaseURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	switch u.Hostname() {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com":
		return true
	default:
		return false
	}
}

func githubGet(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil || !githubReleaseURL(req.URL) {
		return nil, errors.New("invalid GitHub release URL")
	}
	req.Header.Set("User-Agent", "BMCShell")
	if req.URL.Hostname() == "api.github.com" {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("GitHub release request failed")
	}
	return response, nil
}

func saveGitHubAsset(ctx context.Context, client *http.Client, address, path string, limit int64) error {
	response, err := githubGet(ctx, client, address)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub release asset returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return errors.New("GitHub release asset exceeds the size limit")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	size, copyErr := io.Copy(file, io.LimitReader(response.Body, limit+1))
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return err
	}
	if size > limit {
		return errors.New("GitHub release asset exceeds the size limit")
	}
	return nil
}
