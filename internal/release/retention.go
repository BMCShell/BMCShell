package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const (
	historyName     = "release-history.json"
	historySchema   = 1
	maxHistoryBytes = 1 << 20
)

type verifiedRelease struct {
	Version string `json:"version"`
	Digest  string `json:"sha256"`
}

type releaseHistory struct {
	Schema     int               `json:"schema"`
	Active     verifiedRelease   `json:"active"`
	MarkerTime int64             `json:"activeMarkerTime"`
	Previous   *verifiedRelease  `json:"previous,omitempty"`
	Superseded []verifiedRelease `json:"superseded,omitempty"`
}

// PlanRetention returns only the core payloads positively superseded by
// recorded updates. The caller must stop other users of the installation and
// revalidate the plan before archiving any paths. This function is read-only.
func PlanRetention(installationRoot string) ([]string, error) {
	root, err := filepath.Abs(installationRoot)
	if err != nil {
		return nil, err
	}
	bin := filepath.Join(root, binDir)
	releases := filepath.Join(bin, releasesDir)
	for _, path := range []string{root, bin, releases} {
		if err := directDirectory(path); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	}
	history, ok := currentHistory(bin)
	if !ok || history.Previous == nil || len(history.Superseded) == 0 ||
		!matchingPayload(bin, history.Active) || !matchingPayload(bin, *history.Previous) {
		return nil, nil
	}
	var eligible []string
	for _, release := range history.Superseded {
		if matchingPayload(bin, release) {
			eligible = append(eligible, filepath.Join(releases, release.Version))
		}
	}
	slices.Sort(eligible)
	return eligible, nil
}

func planActivation(bin string, next verifiedRelease) releaseHistory {
	result := releaseHistory{Schema: historySchema, Active: next}
	version, _, err := readActive(bin)
	if err != nil {
		return result
	}
	history, known := currentHistory(bin)
	if strings.EqualFold(version, next.Version) {
		if known {
			result.Previous = history.Previous
			result.Superseded = history.Superseded
		}
		return result
	}
	if known {
		if matchingPayload(bin, history.Active) {
			result.Previous = &history.Active
		} else if history.Previous != nil && !strings.EqualFold(history.Previous.Version, next.Version) &&
			matchingPayload(bin, *history.Previous) {
			result.Previous = history.Previous
		}
		if result.Previous == nil {
			return result
		}
		historyCandidates := slices.Clone(history.Superseded)
		if history.Previous != nil {
			historyCandidates = append(historyCandidates, *history.Previous)
		}
		for _, older := range historyCandidates {
			if strings.EqualFold(older.Version, result.Previous.Version) || strings.EqualFold(older.Version, next.Version) {
				continue
			}
			if _, err := corePayload(bin, older.Version); err == nil {
				result.Superseded = append(result.Superseded, older)
			}
		}
		return result
	}

	// An older installer has no ledger. Verify its active release as the
	// predecessor, while leaving its other payload directories unmanaged.
	payload, err := corePayload(bin, version)
	if err != nil || verifyExecutable(filepath.Join(payload, executableName), version) != nil {
		return result
	}
	digest, err := payloadDigest(payload)
	if err == nil {
		result.Previous = &verifiedRelease{Version: version, Digest: digest}
	}
	return result
}

func currentHistory(bin string) (releaseHistory, bool) {
	active, modified, err := readActive(bin)
	if err != nil {
		return releaseHistory{}, false
	}
	data, _, err := readRegular(filepath.Join(bin, historyName), maxHistoryBytes)
	var history releaseHistory
	if err != nil || json.Unmarshal(data, &history) != nil ||
		history.Schema != historySchema || history.Active.Version != active || history.MarkerTime != modified {
		return releaseHistory{}, false
	}
	seen := make(map[string]bool)
	versions := append([]verifiedRelease{history.Active}, history.Superseded...)
	if history.Previous != nil {
		versions = append(versions, *history.Previous)
	}
	for _, release := range versions {
		key := strings.ToLower(release.Version)
		digest, err := hex.DecodeString(release.Digest)
		if !validVersion(release.Version) || err != nil || len(digest) != sha256.Size || seen[key] {
			return releaseHistory{}, false
		}
		seen[key] = true
	}
	return history, true
}

func readActive(bin string) (string, int64, error) {
	data, info, err := readRegular(filepath.Join(bin, activeRelease), maxVersionLength+1)
	if err != nil {
		return "", 0, err
	}
	version, terminated := strings.CutSuffix(string(data), "\n")
	if !terminated || !validVersion(version) {
		return "", 0, errors.New("invalid BMCShell active version")
	}
	return version, info.ModTime().UnixNano(), nil
}

func readRegular(path string, limit int64) ([]byte, fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, nil, fmt.Errorf("invalid core release metadata file %q", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	openInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openInfo) {
		return nil, nil, fmt.Errorf("core release metadata file changed %q", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) != info.Size() {
		return nil, nil, fmt.Errorf("core release metadata file changed %q", path)
	}
	return data, info, nil
}

func directDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("unsafe core release directory %q", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path && !(runtime.GOOS == "windows" && strings.EqualFold(resolved, path)) {
		return fmt.Errorf("unsafe redirected core release directory %q", path)
	}
	return nil
}

func corePayload(bin, version string) (string, error) {
	if !validVersion(version) {
		return "", fmt.Errorf("invalid core release version %q", version)
	}
	payload := filepath.Join(bin, releasesDir, version)
	if err := directDirectory(payload); err != nil {
		return "", err
	}
	bridge := filepath.Join(payload, filepath.FromSlash(bridgeName))
	if err := directDirectory(filepath.Dir(bridge)); err != nil {
		return "", err
	}
	if !regularFile(bridge) || !regularFile(filepath.Join(payload, executableName)) {
		return "", fmt.Errorf("incomplete core release %q", version)
	}
	return payload, nil
}

func matchingPayload(bin string, release verifiedRelease) bool {
	payload, err := corePayload(bin, release.Version)
	if err != nil {
		return false
	}
	digest, err := payloadDigest(payload)
	return err == nil && strings.EqualFold(digest, release.Digest)
}

func saveHistory(bin string, history releaseHistory) error {
	path := filepath.Join(bin, historyName)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("invalid core release history file %q", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(bin, ".release-history-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(history)
	if err == nil {
		err = file.Sync()
	}
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
