package packages

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const originFile = ".bmc-source"

var originName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func readSource(root string) (string, error) {
	path := filepath.Join(root, originFile)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 49 {
		return "", errors.New("invalid installed package source marker")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	source := strings.TrimSuffix(string(data), "\n")
	if !originName.MatchString(source) {
		return "", errors.New("invalid installed package source name")
	}
	return source, nil
}

func writeSource(root, source string) error {
	if !originName.MatchString(source) {
		return fmt.Errorf("invalid package source name %q", source)
	}
	return os.WriteFile(filepath.Join(root, originFile), []byte(source+"\n"), 0600)
}
