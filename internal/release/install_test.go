package release

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallRejectsUnsafeArchiveWithoutChangingActiveRelease(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("core release installation is for Windows x64")
	}
	install := t.TempDir()
	bin := filepath.Join(install, binDir)
	const current = "3.0.0-dev.5"
	runtimeDir := filepath.Join(bin, releasesDir, current)
	executable := filepath.Join(runtimeDir, executableName)
	bridge := filepath.Join(runtimeDir, filepath.FromSlash(bridgeName))
	if err := os.MkdirAll(filepath.Dir(bridge), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{executable, bridge, filepath.Join(bin, launcherName)} {
		if err := os.WriteFile(path, []byte("installed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const original = current + "\n"
	marker := filepath.Join(bin, activeRelease)
	if err := os.WriteFile(marker, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		entry string
		valid bool
		want  string
	}{
		{"checksum", "BMCShell/BMCShell.exe", false, "SHA-256 与清单不一致"},
		{"legacy", "BMCShell/BMCShell.exe", true, "独立的 BMCSHELL_HOME"},
		{"traversal", "BMCShell/bin/releases/3.0.0/../outside.txt", true, "发布 ZIP 中的条目"},
		{"device", "BMCShell/bin/releases/3.0.0/doc/NUL.txt", true, "发布 ZIP 中的条目"},
		{"private path", "BMCShell/bin/releases/3.0.0/build/BMCShell.exe", true, "发布 ZIP 中的条目"},
		{"other release", "BMCShell/bin/releases/3.0.1/BMCShell.exe", true, "发布 ZIP 中的条目"},
		{"invalid marker", "BMCShell/bin/active", true, "缺少有效的 bin/active 版本标记"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			archivePath := filepath.Join(dir, "release.zip")
			writeReleaseFixture(t, archivePath, tc.entry, tc.valid)
			if _, err := Install(executable, archivePath, io.Discard); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Install() error = %v, want %q", err, tc.want)
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != original {
				t.Fatalf("active marker after failed update = %q, %v", data, err)
			}
			entries, err := os.ReadDir(filepath.Join(bin, releasesDir))
			if err != nil || len(entries) != 1 || entries[0].Name() != current {
				t.Fatalf("staged files after failed update = %v, %v", entries, err)
			}
			staging, err := filepath.Glob(filepath.Join(install, ".core-update-*"))
			if err != nil || len(staging) != 0 {
				t.Fatalf("temporary files after failed update = %v, %v", staging, err)
			}
		})
	}
}

func TestPublishReleaseKeepsInstalledVersionImmutable(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "3.0.0-dev.5")
	source := filepath.Join(root, "candidate")
	write := func(dir, content string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, executableName), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(source, "original")
	if err := publishRelease(source, destination); err != nil {
		t.Fatal(err)
	}
	write(source, "original")
	if err := publishRelease(source, destination); err != nil {
		t.Fatalf("repeat update with identical release: %v", err)
	}
	write(source, "different")
	if err := publishRelease(source, destination); err == nil || !strings.Contains(err.Error(), "已存在且内容不同") {
		t.Fatalf("update with different contents: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(destination, executableName))
	if err != nil || string(content) != "original" {
		t.Fatalf("installed release = %q, %v", content, err)
	}
}

func writeReleaseFixture(t *testing.T, path, entry string, valid bool) {
	t.Helper()
	var content bytes.Buffer
	archive := zip.NewWriter(&content)
	file, err := archive.Create(entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(content.Bytes())
	if !valid {
		hash[0] ^= 1
	}
	m := manifest{
		Schema: 1, Product: "BMCShell", Version: "3.0.0", Platform: "windows-amd64",
		Artifact: filepath.Base(path), SHA256: hex.EncodeToString(hash[:]),
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(path, ".zip")+".manifest.json", data, 0600); err != nil {
		t.Fatal(err)
	}
}
