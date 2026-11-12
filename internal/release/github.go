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
	"bmcshell/internal/httpretry"
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
	return SelectIndexWithProgress(ctx, client, version, nil)
}

func SelectIndexWithProgress(ctx context.Context, client *http.Client, version string, progress io.Writer) (GitHubRelease, error) {
	if version != "" {
		version = strings.TrimPrefix(version, "v")
		if err := packages.ValidateVersion(version); err != nil {
			return GitHubRelease{}, err
		}
	}
	index, err := fetchReleaseIndex(ctx, githubClient(client), progress)
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

func fetchReleaseIndex(ctx context.Context, client *http.Client, progress io.Writer) (releaseIndex, error) {
	var index releaseIndex
	var data []byte
	const maxIndexBytes = 8 << 20
	err := githubGet(ctx, client, coreReleaseIndex, maxIndexBytes, progress, "框架发行版索引", func(body io.Reader) error {
		var err error
		data, err = io.ReadAll(io.LimitReader(body, maxIndexBytes+1))
		return err
	})
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
func (r GitHubRelease) Download(ctx context.Context, client *http.Client, directory string, progress io.Writer) (string, error) {
	client = githubClient(client)
	if progress == nil {
		progress = io.Discard
	}
	archive := filepath.Join(directory, r.archiveName)
	manifestPath := strings.TrimSuffix(archive, ".zip") + ".manifest.json"
	fmt.Fprintln(progress, "正在下载发布校验清单…")
	if _, err := saveGitHubAsset(ctx, client, r.manifestURL, manifestPath, maxManifestBytes, progress, "框架发布校验清单"); err != nil {
		return "", fmt.Errorf("下载发布校验清单失败：%w", err)
	}
	manifest, err := readManifest(archive)
	if err != nil {
		return "", err
	}
	if manifest.Version != r.Version || !strings.EqualFold(manifest.SHA256, r.sha256) {
		return "", errors.New("发布校验清单的版本或 SHA-256 与框架发行版索引不一致")
	}
	fmt.Fprintf(progress, "正在下载框架发布包 %s（%.1f MiB）…\n", r.archiveName, float64(r.bytes)/(1<<20))
	size, err := saveGitHubAsset(ctx, client, r.archiveURL, archive, r.bytes, progress, "框架发布 ZIP")
	if err != nil {
		return "", fmt.Errorf("下载框架发布包失败：%w", err)
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

func githubGet(ctx context.Context, client *http.Client, address string, limit int64, progress io.Writer, label string, consume func(io.Reader) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil || !githubReleaseURL(req.URL) {
		return errors.New("GitHub 发布地址无效")
	}
	req.Header.Set("User-Agent", "BMCShell")
	return httpretry.Get(client, req, limit, progress, label, consume)
}

func saveGitHubAsset(ctx context.Context, client *http.Client, address, path string, limit int64, progress io.Writer, label string) (int64, error) {
	var size int64
	err := githubGet(ctx, client, address, limit, progress, label, func(body io.Reader) error {
		var err error
		size, err = writeGitHubAsset(body, path, limit)
		return err
	})
	return size, err
}

func writeGitHubAsset(body io.Reader, path string, limit int64) (int64, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 0, err
	}
	size, copyErr := io.Copy(file, io.LimitReader(body, limit+1))
	if err := errors.Join(copyErr, file.Close()); err != nil {
		if removeErr := os.Remove(path); removeErr != nil {
			return 0, fmt.Errorf("无法清理未完成的下载：%w", removeErr)
		}
		return 0, err
	}
	if size > limit {
		_ = os.Remove(path)
		return 0, errors.New("GitHub 发布文件超过大小限制")
	}
	return size, nil
}
