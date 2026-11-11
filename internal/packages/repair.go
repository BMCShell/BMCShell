package packages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"
)

const maxRepairListBytes = 64 << 10

var (
	repairDependency = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,63}$`)
	repairPlatform   = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9]+$`)
	repairPackage    = regexp.MustCompile(`^msys/[a-z0-9][a-z0-9._+-]{0,127}$`)
)

type repairList struct {
	Schema      int                `json:"schema"`
	Suggestions []repairSuggestion `json:"suggestions"`
}

type repairSuggestion struct {
	Dependency string `json:"dependency"`
	Suggestion string `json:"suggestion"`
	Platform   string `json:"platform,omitempty"`
	Manager    string `json:"manager,omitempty"`
	Package    string `json:"package,omitempty"`
}

func ParseRepairList(source string, data []byte) ([]RepairRecord, error) {
	if len(data) > maxRepairListBytes {
		return nil, errors.New("repair suggestions exceed 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var list repairList
	if err := decoder.Decode(&list); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("repair suggestions must contain one JSON object")
	}
	if list.Schema != 1 || list.Suggestions == nil || len(list.Suggestions) > 128 {
		return nil, errors.New("repair suggestions require schema 1 and an array of at most 128 entries")
	}
	records := make([]RepairRecord, 0, len(list.Suggestions))
	for i, item := range list.Suggestions {
		if !repairDependency.MatchString(item.Dependency) ||
			strings.TrimSpace(item.Suggestion) == "" || len(item.Suggestion) > 1200 ||
			strings.ContainsFunc(item.Suggestion, unicode.IsControl) ||
			item.Platform != "" && (len(item.Platform) > 40 || !repairPlatform.MatchString(item.Platform)) {
			return nil, fmt.Errorf("invalid repair suggestion at entry %d", i+1)
		}
		if item.Manager != "" || item.Package != "" {
			if item.Manager != "msys2" || item.Platform != "windows-amd64" || !repairPackage.MatchString(item.Package) {
				return nil, fmt.Errorf("invalid package manager recipe at entry %d", i+1)
			}
		}
		records = append(records, RepairRecord{
			SourceID: source, Dependency: item.Dependency, Suggestion: item.Suggestion,
			Platform: item.Platform, Manager: item.Manager, Package: item.Package,
		})
	}
	return records, nil
}

func (p Package) RepairRecords() ([]RepairRecord, error) {
	if p.RepairSuggestions == "" {
		return nil, nil
	}
	records, err := p.loadRepairRecords()
	if err != nil {
		return nil, fmt.Errorf("%s repair suggestions: %w", p.Name, err)
	}
	return records, nil
}

func (p Package) loadRepairRecords() ([]RepairRecord, error) {
	path, err := p.File(p.RepairSuggestions)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRepairListBytes+1))
	if err != nil {
		return nil, err
	}
	records, err := ParseRepairList(p.ID, data)
	if err != nil {
		return nil, err
	}
	dependencies := make(map[string]bool, len(p.SystemDependencies))
	for _, dependency := range p.SystemDependencies {
		dependencies[dependency.ID] = true
	}
	for _, record := range records {
		if !dependencies[record.Dependency] {
			return nil, fmt.Errorf("repair suggestion references undeclared dependency %q", record.Dependency)
		}
	}
	return records, nil
}
