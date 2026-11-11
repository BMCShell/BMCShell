package release

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode"

	"bmcshell/internal/filelock"
	"bmcshell/internal/packages"
)

const (
	executableName    = "BMCShell.exe"
	bridgeName        = "shell/bmcshell.sh"
	legacyReleasesDir = ".releases"
	legacyMarker      = ".active-release"
	binDir            = "bin"
	launcherName      = "bmcshell.sh"
	releasesDir       = "releases"
	activeRelease     = "active"
	maxVersionLength  = 128
	maxArchiveBytes   = 128 << 20
	maxExpandedBytes  = 256 << 20
)

var (
	releaseDirName = regexp.MustCompile(`^release-[0-9]+$`)
	reservedDevice = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|COM[1-9¹²³]|LPT[1-9¹²³])$`)
)

type manifest struct {
	Schema   int    `json:"schema"`
	Product  string `json:"product"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
}

// CheckInstallation verifies that the running executable belongs to an
// extracted release rather than a source build.
func CheckInstallation(executable string) error {
	_, err := installationRoot(executable)
	return err
}

// Install verifies the full release before publishing a versioned payload and
// selecting it for new shells. Existing releases and runtime data are retained.
func Install(executable, archivePath string) (string, error) {
	install, err := installationRoot(executable)
	if err != nil {
		return "", err
	}
	unlock, err := filelock.Acquire(filepath.Join(install, ".release-update.lock"))
	if err != nil {
		return "", fmt.Errorf("lock BMCShell release update: %w", err)
	}
	defer unlock()
	m, err := readManifest(archivePath)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(install, ".core-update-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := unpackVerified(archivePath, m, stage); err != nil {
		return "", err
	}

	bin := filepath.Join(install, binDir)
	releases := filepath.Join(bin, releasesDir)
	for _, directory := range []string{bin, releases} {
		if err := ensureDirectory(directory); err != nil {
			return "", err
		}
	}
	payload := filepath.Join(stage, binDir, releasesDir, m.Version)
	digest, err := payloadDigest(payload)
	if err != nil {
		return "", err
	}
	history := planActivation(bin, verifiedRelease{Version: m.Version, Digest: digest})
	if err := publishRelease(payload, filepath.Join(releases, m.Version)); err != nil {
		return "", err
	}
	if err := publishLauncher(filepath.Join(stage, binDir, launcherName), filepath.Join(bin, launcherName)); err != nil {
		return "", err
	}
	if err := activate(bin, history); err != nil {
		return "", err
	}
	return m.Version, nil
}

// PortableRoot recognizes a native executable in bin/releases/<version>.
// It does not depend on the active marker, since a running Bash can retain an
// earlier version after another Bash changes the marker.
func PortableRoot(executable string) (string, bool) {
	versionDir := filepath.Dir(executable)
	releases := filepath.Dir(versionDir)
	bin := filepath.Dir(releases)
	if !strings.EqualFold(filepath.Base(executable), executableName) ||
		!validVersion(filepath.Base(versionDir)) ||
		!strings.EqualFold(filepath.Base(releases), releasesDir) ||
		!strings.EqualFold(filepath.Base(bin), binDir) {
		return "", false
	}
	return filepath.Dir(bin), true
}

func validVersion(version string) bool {
	return len(version) <= maxVersionLength && packages.ValidateVersion(version) == nil
}

func installationRoot(executable string) (string, error) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return "", errors.New("core release updates require Windows x64")
	}
	path, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	directory := filepath.Dir(path)
	if root, portable := PortableRoot(path); portable {
		directory = root
		// A retained executable can restore a missing launcher and active marker.
	} else {
		if releaseDirName.MatchString(filepath.Base(directory)) &&
			filepath.Base(filepath.Dir(directory)) == legacyReleasesDir {
			directory = filepath.Dir(filepath.Dir(directory))
		}
		if !strings.EqualFold(filepath.Base(path), executableName) ||
			!regularFile(filepath.Join(directory, executableName)) ||
			!regularFile(filepath.Join(directory, filepath.FromSlash(bridgeName))) {
			return "", errors.New("core updates require an extracted BMCShell release")
		}
	}
	if _, err := os.Lstat(filepath.Join(directory, ".git")); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("core updates require an extracted release outside the source checkout")
	}
	return directory, nil
}

func ensureDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("invalid core release directory %q", path)
	}
	return nil
}

func publishRelease(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	} else if _, lookupErr := os.Lstat(destination); lookupErr != nil {
		return err
	}
	// Reusing an installed version is safe only when all its runtime files
	// match the freshly verified archive. Never replace a running executable.
	existing, err := payloadDigest(destination)
	if err != nil {
		return err
	}
	incoming, err := payloadDigest(source)
	if err != nil {
		return err
	}
	if existing != incoming {
		return fmt.Errorf("core release %s already exists with different contents; close BMCShell terminals and follow the release recovery instructions", filepath.Base(destination))
	}
	return nil
}

func publishLauncher(source, destination string) error {
	// Only replace a regular file, or create a new launcher. The staged
	// launcher can also repair an empty or modified existing script.
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("invalid portable BMCShell launcher %q", destination)
		}
		incoming, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if info.Size() == int64(len(incoming)) {
			existing, err := os.ReadFile(destination)
			if err != nil {
				return err
			}
			if bytes.Equal(existing, incoming) {
				return nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}

func payloadDigest(root string) (string, error) {
	hash := sha256.New()
	remaining := int64(maxExpandedBytes)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			fmt.Fprintf(hash, "directory %q\n", filepath.ToSlash(relative))
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() > remaining {
			return errors.New("invalid installed core release file")
		}
		fmt.Fprintf(hash, "file %q %d\n", filepath.ToSlash(relative), info.Size())
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(hash, io.LimitReader(file, info.Size()+1))
		if err := errors.Join(copyErr, file.Close()); err != nil {
			return err
		}
		if n != info.Size() {
			return errors.New("installed core release changed during verification")
		}
		remaining -= n
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil)), err
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() != 0
}

func readManifest(archivePath string) (manifest, error) {
	var m manifest
	ext := filepath.Ext(archivePath)
	if !strings.EqualFold(ext, ".zip") {
		return m, errors.New("core update requires a release ZIP file")
	}
	path := strings.TrimSuffix(archivePath, ext) + ".manifest.json"
	file, err := os.Open(path)
	if err != nil {
		return m, fmt.Errorf("read release checksum manifest: %w", err)
	}
	defer file.Close()
	const maxManifestBytes = 16 << 10
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return m, err
	}
	if len(data) > maxManifestBytes || json.Unmarshal(data, &m) != nil ||
		m.Schema != 1 || m.Product != "BMCShell" || m.Platform != "windows-amd64" ||
		m.Artifact != filepath.Base(archivePath) || !validVersion(m.Version) {
		return m, errors.New("invalid or mismatched BMCShell release manifest")
	}
	if hash, err := hex.DecodeString(m.SHA256); err != nil || len(hash) != sha256.Size {
		return m, errors.New("invalid release SHA-256")
	}
	return m, nil
}

func unpackVerified(archivePath string, m manifest, stage string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxArchiveBytes {
		return errors.New("invalid or oversized BMCShell release ZIP")
	}

	// Hash and unpack the same private snapshot, even if the input is edited
	// while the update is being prepared.
	snapshot, err := os.CreateTemp(filepath.Dir(stage), ".archive-")
	if err != nil {
		return err
	}
	defer os.Remove(snapshot.Name())
	defer snapshot.Close()
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(snapshot, digest), io.LimitReader(archive, maxArchiveBytes+1))
	if err != nil {
		return err
	}
	if size > maxArchiveBytes || !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), m.SHA256) {
		return errors.New("BMCShell release ZIP does not match the manifest SHA-256")
	}
	zipped, err := zip.NewReader(snapshot, size)
	if err != nil {
		return fmt.Errorf("read BMCShell release ZIP: %w", err)
	}
	for _, file := range zipped.File {
		if file.Name == "BMCShell/"+executableName {
			return errors.New("legacy core releases require their original installation and an independent BMCSHELL_HOME; use a portable bin-layout release for this update")
		}
	}
	if err := extract(zipped.File, stage, m.Version); err != nil {
		return err
	}
	bin := filepath.Join(stage, binDir)
	markerPath := filepath.Join(bin, activeRelease)
	if info, err := os.Lstat(markerPath); err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(m.Version)+1) {
		return errors.New("release is missing a valid bin/active version marker")
	}
	if marker, err := os.ReadFile(markerPath); err != nil || string(marker) != m.Version+"\n" {
		return errors.New("release bin/active marker differs from the manifest version")
	}
	payload := filepath.Join(bin, releasesDir, m.Version)
	executable := filepath.Join(payload, executableName)
	if !regularFile(filepath.Join(bin, launcherName)) ||
		!regularFile(executable) || !regularFile(filepath.Join(payload, filepath.FromSlash(bridgeName))) {
		return errors.New("release is missing its launcher, executable, or Bash adapter")
	}
	return verifyExecutable(executable, m.Version)
}

func verifyExecutable(executable, version string) error {
	binary, err := pe.Open(executable)
	if err != nil {
		return errors.New("release BMCShell.exe is not a Windows x64 executable")
	}
	defer binary.Close()
	if _, ok := binary.OptionalHeader.(*pe.OptionalHeader64); !ok ||
		binary.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || binary.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 ||
		binary.Characteristics&pe.IMAGE_FILE_DLL != 0 {
		return errors.New("release BMCShell.exe is not a Windows x64 executable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, "--version").Output()
	if err != nil || strings.TrimSpace(string(output)) != "BMCShell "+version {
		return errors.New("release executable did not report the manifest version")
	}
	return nil
}

func extract(files []*zip.File, stage, version string) error {
	if len(files) == 0 || len(files) > 512 {
		return errors.New("unexpected number of files in the release ZIP")
	}
	root, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer root.Close()
	seen := make(map[string]bool)
	remaining := uint64(maxExpandedBytes)
	for _, file := range files {
		isDirectory := file.FileInfo().IsDir()
		name, ok := strings.CutPrefix(file.Name, "BMCShell/")
		if ok && isDirectory && name == "" {
			continue
		}
		if isDirectory {
			name = strings.TrimSuffix(name, "/")
		}
		local, err := filepath.Localize(name)
		if !ok || err != nil || !validReleaseEntry(name, isDirectory, version) || seen[strings.ToLower(name)] {
			return fmt.Errorf("invalid or repeated release ZIP entry %q", file.Name)
		}
		seen[strings.ToLower(name)] = true
		if isDirectory {
			if err := root.MkdirAll(local, 0700); err != nil {
				return err
			}
			continue
		}
		if !file.Mode().IsRegular() || file.UncompressedSize64 > remaining {
			return fmt.Errorf("invalid or oversized release ZIP entry %q", file.Name)
		}
		if err := root.MkdirAll(filepath.Dir(local), 0700); err != nil {
			return err
		}
		if err := writeArchiveFile(root, file, local); err != nil {
			return err
		}
		remaining -= file.UncompressedSize64
	}
	return nil
}

func validReleaseEntry(name string, directory bool, version string) bool {
	payload := binDir + "/" + releasesDir + "/" + version
	switch name {
	case binDir, binDir + "/" + releasesDir, payload:
		return directory
	case binDir + "/" + launcherName, binDir + "/" + activeRelease:
		return !directory
	default:
		relative, ok := strings.CutPrefix(name, payload+"/")
		return ok && validArchivePath(relative)
	}
}

func validArchivePath(path string) bool {
	first, _, _ := strings.Cut(path, "/")
	switch strings.ToLower(first) {
	case legacyReleasesDir, legacyMarker, ".git", "build", binDir:
		return false
	}
	for part := range strings.SplitSeq(path, "/") {
		base, _, _ := strings.Cut(part, ".")
		if strings.TrimRight(part, " .") != part || strings.ContainsAny(part, "\\:*?\"<>|") ||
			strings.ContainsFunc(part, unicode.IsControl) || reservedDevice.MatchString(strings.TrimRight(base, " ")) {
			return false
		}
	}
	return true
}

func writeArchiveFile(root *os.Root, file *zip.File, name string) error {
	input, err := file.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	n, err := io.Copy(output, io.LimitReader(input, int64(file.UncompressedSize64)+1))
	if err == nil && n != int64(file.UncompressedSize64) {
		err = errors.New("release ZIP entry size mismatch")
	}
	return errors.Join(err, output.Close())
}

func activate(bin string, history releaseHistory) error {
	marker := filepath.Join(bin, activeRelease)
	if info, err := os.Lstat(marker); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("invalid BMCShell active release marker %q", marker)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(bin, ".active-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = fmt.Fprintln(file, history.Active.Version)
	if err == nil {
		err = file.Sync()
	}
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	info, err := os.Lstat(file.Name())
	if err != nil {
		return err
	}
	history.MarkerTime = info.ModTime().UnixNano()
	// A crash before the marker replacement leaves a mismatched ledger.
	// Retention refuses to act on that state.
	if err := saveHistory(bin, history); err != nil {
		return fmt.Errorf("save core release history: %w", err)
	}
	return os.Rename(file.Name(), marker)
}
