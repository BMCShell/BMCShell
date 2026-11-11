package release

import (
	"context"
	"encoding/hex"
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

const (
	coreReleaseDownload = "https://github.com/BMCShell/BMCShell/releases/download/"
	coreReleaseIndex    = coreReleaseDownload + "release-index/index.json"
	windowsAMD64        = "windows-amd64"
)

type releaseIndex struct {
	Schema   int              `json:"schema"`
	Product  string           `json:"product"`
	Releases []indexedRelease `json:"releases"`
}

type indexedRelease struct {
	Version   string            `json:"version"`
	Tag       string            `json:"tag"`
	Artifacts []indexedArtifact `json:"artifacts"`
}

type indexedArtifact struct {
	Platform    string `json:"platform"`
	URL         string `json:"url"`
	ManifestURL string `json:"manifestUrl"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
}

// GitHubRelease describes a compatible release and its paired assets.
type GitHubRelease struct {
	Version                              string
	archiveName, archiveURL, manifestURL string
	sha256                               string
	bytes                                int64
}

func githubClient(client *http.Client) *http.Client {
	copy := *client
	copy.CheckRedirect = func(req *http.Request, previous []*http.Request) error {
		if len(previous) >= 10 || !githubReleaseURL(req.URL) {
			return errors.New("GitHub 发布请求重定向到不受支持的地址")
		}
		req.Header.Del("Referer")
		return nil
	}
	return &copy
}

// SelectIndex reads the public core release index without downloading the selected assets.
func SelectIndex(ctx context.Context, client *http.Client, version string) (GitHubRelease, error) {
	if version != "" {
		version = strings.TrimPrefix(version, "v")
		if err := packages.ValidateVersion(version); err != nil {
			return GitHubRelease{}, err
		}
	}
	index, err := fetchReleaseIndex(ctx, githubClient(client))
	if err != nil {
		return GitHubRelease{}, err
	}
	var selected GitHubRelease
	for _, item := range index.Releases {
		if version != "" && item.Version != version {
			continue
		}
		assets, err := item.windowsRelease()
		if err != nil {
			return GitHubRelease{}, err
		}
		if assets.Version != "" && (selected.Version == "" || catalog.CompareVersion(assets.Version, selected.Version) > 0) {
			selected = assets
		}
	}
	if selected.Version != "" {
		return selected, nil
	}
	if version != "" {
		return GitHubRelease{}, fmt.Errorf("框架发行版索引中没有版本 %s 的 Windows x64 发布包", version)
	}
	return GitHubRelease{}, errors.New("框架发行版索引中没有 Windows x64 发布包")
}

func fetchReleaseIndex(ctx context.Context, client *http.Client) (releaseIndex, error) {
	var index releaseIndex
	response, err := githubGet(ctx, client, coreReleaseIndex)
	if err != nil {
		return index, fmt.Errorf("读取框架发行版索引失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return index, fmt.Errorf("读取框架发行版索引失败，HTTP %d", response.StatusCode)
	}
	const maxIndexBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxIndexBytes+1))
	if err != nil {
		return index, fmt.Errorf("读取框架发行版索引失败：%w", err)
	}
	if len(data) > maxIndexBytes || json.Unmarshal(data, &index) != nil || index.Schema != 1 || index.Product != "BMCShell" || index.Releases == nil {
		return index, errors.New("框架发行版索引无效或超过大小限制")
	}
	return index, nil
}

func (item indexedRelease) windowsRelease() (GitHubRelease, error) {
	var selected GitHubRelease
	for _, asset := range item.Artifacts {
		if asset.Platform != windowsAMD64 {
			continue
		}
		base := "BMCShell-" + item.Version + "-" + windowsAMD64
		archive := base + ".zip"
		if selected.Version != "" || !validVersion(item.Version) ||
			(item.Tag != item.Version && item.Tag != "v"+item.Version) ||
			asset.Bytes <= 0 || asset.Bytes > maxArchiveBytes ||
			!validReleaseDigest(asset.SHA256) ||
			!indexedAssetURL(asset.URL, item.Tag, archive) ||
			!indexedAssetURL(asset.ManifestURL, item.Tag, base+".manifest.json") {
			return GitHubRelease{}, fmt.Errorf("框架发行版索引中版本 %q 的 Windows x64 发布包信息无效", item.Version)
		}
		selected = GitHubRelease{
			Version: item.Version, archiveName: archive, archiveURL: asset.URL, manifestURL: asset.ManifestURL,
			sha256: asset.SHA256, bytes: asset.Bytes,
		}
	}
	return selected, nil
}

func validReleaseDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func indexedAssetURL(address, tag, filename string) bool {
	parsed, err := url.Parse(address)
	return err == nil && githubReleaseURL(parsed) && parsed.Hostname() == "github.com" &&
		!parsed.ForceQuery && parsed.RawQuery == "" &&
		parsed.Path == "/BMCShell/BMCShell/releases/download/"+tag+"/"+filename
}

// Download retrieves the selected assets into a private staging directory.
func (r GitHubRelease) Download(ctx context.Context, client *http.Client, directory string) (string, error) {
	client = githubClient(client)
	archive := filepath.Join(directory, r.archiveName)
	manifestPath := strings.TrimSuffix(archive, ".zip") + ".manifest.json"
	if _, err := saveGitHubAsset(ctx, client, r.manifestURL, manifestPath, maxManifestBytes); err != nil {
		return "", err
	}
	manifest, err := readManifest(archive)
	if err != nil {
		return "", err
	}
	if manifest.Version != r.Version || !strings.EqualFold(manifest.SHA256, r.sha256) {
		return "", errors.New("发布校验清单的版本或 SHA-256 与框架发行版索引不一致")
	}
	size, err := saveGitHubAsset(ctx, client, r.archiveURL, archive, r.bytes)
	if err != nil {
		return "", err
	}
	if size != r.bytes {
		return "", errors.New("发布 ZIP 的大小与框架发行版索引不一致")
	}
	return archive, nil
}

func githubReleaseURL(u *url.URL) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	switch u.Hostname() {
	case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com":
		return true
	default:
		return false
	}
}

func githubGet(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil || !githubReleaseURL(req.URL) {
		return nil, errors.New("GitHub 发布地址无效")
	}
	req.Header.Set("User-Agent", "BMCShell")
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("请求 GitHub 发布文件失败")
	}
	return response, nil
}

func saveGitHubAsset(ctx context.Context, client *http.Client, address, path string, limit int64) (int64, error) {
	response, err := githubGet(ctx, client, address)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("GitHub 发布文件的下载请求返回 HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return 0, errors.New("GitHub 发布文件超过大小限制")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, err
	}
	size, copyErr := io.Copy(file, io.LimitReader(response.Body, limit+1))
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return 0, err
	}
	if size > limit {
		return 0, errors.New("GitHub 发布文件超过大小限制")
	}
	return size, nil
}
