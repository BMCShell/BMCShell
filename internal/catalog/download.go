package catalog

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"bmcshell/internal/packages"
	"bmcshell/internal/proxy"
)

const (
	maxURLLength     = 4096
	maxArchiveBytes  = 128 << 20
	maxManifestBytes = 1 << 20
)

func networkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > maxURLLength || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("software source URLs must use HTTPS (loopback HTTP is also allowed)")
	}
	if u.Scheme == "https" {
		return u, nil
	}
	if u.Scheme == "http" {
		host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
		addr, _ := netip.ParseAddr(host)
		if host == "localhost" || addr.IsLoopback() {
			return u, nil
		}
	}
	return nil, errors.New("software source URLs must use HTTPS (loopback HTTP is also allowed)")
}

func privateTLSOrigin(u *url.URL) netip.AddrPort {
	if u.Scheme != "https" {
		return netip.AddrPort{}
	}
	address, err := netip.ParseAddr(u.Hostname())
	if err != nil || address.Zone() != "" || !address.IsPrivate() {
		return netip.AddrPort{}
	}
	port := uint64(443)
	if u.Port() != "" {
		port, err = strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port == 0 {
			return netip.AddrPort{}
		}
	}
	return netip.AddrPortFrom(address.Unmap(), uint16(port))
}

type sourceTransport struct {
	origin           netip.AddrPort
	strict, insecure http.RoundTripper
}

func (t sourceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The client invokes RoundTrip again for each redirect.
	if privateTLSOrigin(req.URL) == t.origin {
		return t.insecure.RoundTrip(req)
	}
	return t.strict.RoundTrip(req)
}

func (s *Store) sourceClient(source Source) *http.Client {
	if !source.InsecureTLS {
		return s.client
	}
	u, err := networkURL(source.URL)
	if err != nil {
		return s.client
	}
	origin := privateTLSOrigin(u)
	if !origin.IsValid() {
		return s.client
	}
	client := *s.client
	client.Transport = sourceTransport{origin: origin, strict: client.Transport, insecure: s.insecureTransport()}
	return &client
}

var client = http.Client{
	Transport: proxy.HTTPTransport(""),
	Timeout:   90 * time.Second,
	CheckRedirect: func(req *http.Request, previous []*http.Request) error {
		if len(previous) >= 10 || previous[len(previous)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("unsafe software source redirect")
		}
		if _, err := networkURL(req.URL.String()); err != nil {
			return err
		}
		req.Header.Del("Referer")
		req.Header.Del("Authorization")
		req.Header.Del("Cookie")
		return nil
	},
}

func fetch(ctx context.Context, client *http.Client, raw string, limit int64) (io.ReadCloser, error) {
	u, err := networkURL(raw)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid software source request")
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// net/http errors include the complete URL, which may contain a signed query.
		return nil, errors.New("software source request failed")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("software source returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		response.Body.Close()
		return nil, errors.New("software source data exceeds the size limit")
	}
	return response.Body, nil
}

// Download retrieves a candidate ZIP into an existing staging directory and
// verifies both the bytes and the metadata used to plan the operation.
func Download(ctx context.Context, entry Entry, dir string) (string, error) {
	return download(ctx, &client, entry, dir)
}

func (s *Store) Download(ctx context.Context, entry Entry, dir string) (string, error) {
	if entry.source.InsecureTLS {
		sources, err := s.Sources()
		if err != nil {
			return "", err
		}
		if !slices.Contains(sources, entry.source) {
			return "", errors.New("source changed before package download")
		}
	}
	return download(ctx, s.sourceClient(entry.source), entry, dir)
}

func download(ctx context.Context, client *http.Client, entry Entry, dir string) (string, error) {
	if err := entry.validate(); err != nil {
		return "", err
	}
	response, err := fetch(ctx, client, entry.URL, maxArchiveBytes)
	if err != nil {
		return "", err
	}
	defer response.Close()
	file, err := os.CreateTemp(dir, "package-*.zip")
	if err != nil {
		return "", err
	}
	path := file.Name()
	verified := false
	defer func() {
		if !verified {
			_ = os.Remove(path)
		}
	}()
	digest := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response, maxArchiveBytes+1))
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return "", err
	}
	if size > maxArchiveBytes {
		return "", errors.New("package ZIP exceeds the download size limit")
	}
	if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), entry.SHA256) {
		return "", fmt.Errorf("SHA256 mismatch for %s %s", entry.Name, entry.Version)
	}
	if err := verifyArchive(path, entry); err != nil {
		return "", err
	}
	verified = true
	return path, nil
}

func verifyArchive(path string, entry Entry) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		if archive != nil {
			archive.Close()
		}
		return errors.New("downloaded package is not a valid ZIP")
	}
	defer archive.Close()
	var manifestFile *zip.File
	for _, file := range archive.File {
		if !strings.EqualFold(file.Name, packages.ManifestFile) {
			continue
		}
		if manifestFile != nil || file.Name != packages.ManifestFile || !file.Mode().IsRegular() {
			return errors.New("invalid or repeated package manifest in ZIP")
		}
		manifestFile = file
	}
	if manifestFile == nil || manifestFile.UncompressedSize64 > maxManifestBytes {
		return errors.New("missing or oversized package manifest in ZIP")
	}
	reader, err := manifestFile.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	data, err := readLimited(reader, maxManifestBytes)
	if err != nil {
		return err
	}
	var manifest packages.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return errors.New("invalid package manifest in ZIP")
	}
	version := 0
	if manifest.Library != nil {
		version = manifest.Library.SchemaVersion
	}
	if manifest.Schema != 1 || manifest.ID != entry.ID || manifest.Name != entry.Name ||
		manifest.Type != entry.Type || manifest.Version != entry.Version || manifest.CoreAPI != entry.CoreAPI ||
		version != entry.LibrarySchemaVersion ||
		!equalList(manifest.Platforms, entry.Platforms) || !equalList(manifest.Members, entry.Members) ||
		!maps.Equal(manifest.Dependencies, entry.Dependencies) ||
		!maps.Equal(manifest.LibrarySchemas, entry.LibrarySchemas) ||
		!maps.Equal(manifest.LibraryMigrations, entry.LibraryMigrations) {
		return fmt.Errorf("ZIP manifest does not match the source index for %s %s", entry.Name, entry.Version)
	}
	return nil
}

func equalList(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}
