package packages

import (
	"errors"
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
		return "", errors.New("已安装包的来源标记无效")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	source := strings.TrimSuffix(string(data), "\n")
	if !originName.MatchString(source) {
		return "", errors.New("已安装包的软件源名称无效")
	}
	return source, nil
}
