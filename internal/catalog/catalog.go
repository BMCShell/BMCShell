// Package catalog manages configured software sources and their verified indexes.
package catalog

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"sync"

	"bmcshell/internal/filelock"
	"bmcshell/internal/proxy"
)

const (
	maxIndexBytes  = 8 << 20
	maxConfigBytes = 256 << 10
	maxSources     = 32
)

var sourceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

type Source struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Official    bool   `json:"official,omitempty"`
	InsecureTLS bool   `json:"insecureTls,omitempty"`
}

type RefreshResult struct {
	Source string
	Count  int
	Err    error
}

type Store struct {
	root              string
	client            *http.Client
	insecureTransport func() *http.Transport
}

func Open(home string) *Store {
	return OpenWithProxy(home, "")
}

func OpenWithProxy(home, address string) *Store {
	client := client
	client.Transport = proxy.HTTPTransport(address)
	return &Store{
		root:   filepath.Join(home, "catalog"),
		client: &client,
		insecureTransport: sync.OnceValue(func() *http.Transport {
			transport := client.Transport.(*http.Transport).Clone()
			if transport.TLSClientConfig == nil {
				transport.TLSClientConfig = &tls.Config{}
			}
			// This transport is selected only for the explicitly configured private origin.
			transport.TLSClientConfig.InsecureSkipVerify = true
			return transport
		}),
	}
}

func (s *Store) Sources() ([]Source, error) {
	data, err := readFile(filepath.Join(s.root, "sources.json"), maxConfigBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sources []Source
	if err := json.Unmarshal(data, &sources); err != nil || sources == nil || len(sources) > maxSources {
		return nil, errors.New("软件源配置无效")
	}
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		if err := source.validate(); err != nil {
			return nil, err
		}
		if seen[source.Name] {
			return nil, fmt.Errorf("软件源 %q 重复", source.Name)
		}
		seen[source.Name] = true
	}
	sort.Slice(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if a.Official != b.Official {
			return a.Official
		}
		return a.Name < b.Name
	})
	return sources, nil
}

func (s *Store) SetSource(source Source) error {
	if err := source.validate(); err != nil {
		return err
	}
	return s.withLock(func() error {
		sources, err := s.Sources()
		if err != nil {
			return err
		}
		index := slices.IndexFunc(sources, func(existing Source) bool { return existing.Name == source.Name })
		if index >= 0 {
			if sources[index] == source {
				return nil
			}
			sources[index] = source
		} else {
			if len(sources) >= maxSources {
				return fmt.Errorf("最多可配置 %d 个软件源", maxSources)
			}
			sources = append(sources, source)
		}
		return writeJSON(filepath.Join(s.root, "sources.json"), sources)
	})
}

func (s *Store) RemoveSource(name string) error {
	if !sourceName.MatchString(name) {
		return fmt.Errorf("软件源名称 %q 无效", name)
	}
	return s.withLock(func() error {
		sources, err := s.Sources()
		if err != nil {
			return err
		}
		index := slices.IndexFunc(sources, func(source Source) bool { return source.Name == name })
		if index < 0 {
			return fmt.Errorf("尚未配置软件源 %q", name)
		}
		if err := writeJSON(filepath.Join(s.root, "sources.json"), slices.Delete(sources, index, index+1)); err != nil {
			return err
		}
		// A disabled source is excluded from Entries even if its cache cannot be removed.
		_ = os.Remove(s.indexPath(name))
		return nil
	})
}

func (s *Store) ResetSources() error {
	return s.withLock(func() error {
		return writeJSON(filepath.Join(s.root, "sources.json"), []Source{})
	})
}

func (s *Store) Refresh(ctx context.Context, name string) ([]RefreshResult, error) {
	sources, err := s.Sources()
	if err != nil {
		return nil, err
	}
	if name != "" {
		sources = slices.DeleteFunc(sources, func(source Source) bool { return source.Name != name })
		if len(sources) == 0 {
			return nil, fmt.Errorf("尚未配置软件源 %q", name)
		}
	}
	if len(sources) == 0 {
		return nil, errors.New("尚未配置软件源")
	}
	var results []RefreshResult
	var failures []error
	for _, source := range sources {
		count, err := s.refreshSource(ctx, source)
		results = append(results, RefreshResult{Source: source.Name, Count: count, Err: err})
		if err != nil {
			failures = append(failures, fmt.Errorf("软件源 %s：%w", source.Name, err))
		}
	}
	return results, errors.Join(failures...)
}

type cachedIndex struct {
	URL         string `json:"url"`
	Data        []byte `json:"data"`
	InsecureTLS bool   `json:"insecureTls,omitempty"`
}

func (s *Store) refreshSource(ctx context.Context, source Source) (int, error) {
	response, err := fetch(ctx, s.sourceClient(source), source.URL, maxIndexBytes)
	if err != nil {
		return 0, err
	}
	defer response.Close()
	data, err := readLimited(response, maxIndexBytes)
	if err != nil {
		return 0, err
	}
	entries, err := parseIndex(source, data)
	if err != nil {
		return 0, err
	}
	if err := s.withLock(func() error {
		sources, err := s.Sources()
		if err != nil {
			return err
		}
		if !slices.Contains(sources, source) {
			return errors.New("刷新索引时软件源配置已变更")
		}
		return writeJSON(s.indexPath(source.Name), cachedIndex{URL: source.URL, Data: data, InsecureTLS: source.InsecureTLS})
	}); err != nil {
		return 0, err
	}
	return len(entries), nil
}

func (s *Store) Entries() ([]Entry, error) {
	sources, err := s.Sources()
	if err != nil {
		return nil, err
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH
	var entries []Entry
	for _, source := range sources {
		cached, err := s.CachedEntries(source)
		if err != nil {
			return nil, err
		}
		for _, entry := range cached {
			if entry.CoreAPI == 1 && (len(entry.Platforms) == 0 || slices.Contains(entry.Platforms, "any") || slices.Contains(entry.Platforms, platform)) {
				entries = append(entries, entry)
			}
		}
	}
	return entries, nil
}

// CachedEntries reads every entry in the cached index for a configured source.
// A nil slice with no error means there is no cache matching the configuration.
func (s *Store) CachedEntries(source Source) ([]Entry, error) {
	if err := source.validate(); err != nil {
		return nil, err
	}
	data, err := readFile(s.indexPath(source.Name), maxIndexBytes*2)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var index cachedIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("软件源 %s 的缓存索引无效，请刷新软件源", source.Name)
	}
	if index.URL != source.URL || index.InsecureTLS != source.InsecureTLS {
		return nil, nil
	}
	entries, err := parseIndex(source, index.Data)
	if err != nil {
		return nil, fmt.Errorf("软件源 %s：%w", source.Name, err)
	}
	return entries, nil
}

func (source Source) validate() error {
	if !sourceName.MatchString(source.Name) || source.Official != (source.Name == "official") {
		return fmt.Errorf("软件源名称 %q 无效。official 保留给官方源", source.Name)
	}
	u, err := networkURL(source.URL)
	if err != nil {
		return fmt.Errorf("软件源 %s：%w", source.Name, err)
	}
	if source.InsecureTLS && !privateTLSOrigin(u).IsValid() {
		return fmt.Errorf("软件源 %s 使用 --insecure 时，HTTPS 地址必须直接包含私有 IP", source.Name)
	}
	return nil
}

func (s *Store) indexPath(name string) string {
	return filepath.Join(s.root, "index-"+name+".json")
}

func (s *Store) withLock(action func() error) error {
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	unlock, err := filelock.Acquire(filepath.Join(s.root, ".sources.lock"))
	if err != nil {
		return fmt.Errorf("无法锁定软件源配置：%w", err)
	}
	defer unlock()
	return action()
}

func readFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f, limit)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("软件源数据超过大小限制")
	}
	return data, nil
}

func writeJSON(path string, value any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".source-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(value)
	if err == nil {
		err = f.Sync()
	}
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
