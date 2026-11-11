// Package bookdb reads the book_meta v1 format introduced by BMC Shell 2.x.
package bookdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"bmcshell/internal/envregistry"

	_ "github.com/ncruces/go-sqlite3/driver"
)

const metadataLimit = 256 << 10

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var bookName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

type lookup struct {
	Name       string
	Option     string
	Fields     []string
	Positional bool
}

type fieldOption struct {
	Title         string
	Format        string
	Width         int
	Substitutions map[string]string `json:"substitutions,omitempty"`
}

type metadata struct {
	Title, Name, Description, Source, Table, Body, ContentFormat, Help string
	List, Search, Order                                                []string
	Details                                                            bool
	Lookups                                                            []lookup
	Options                                                            map[string]fieldOption
	fields                                                             map[string]string
}

func databaseURI(path, mode string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	path = filepath.ToSlash(abs)
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath()}
	q := u.Query()
	q.Set("mode", mode)
	q.Add("_pragma", "trusted_schema(OFF)")
	q.Add("_pragma", "busy_timeout(1000)")
	if mode == "ro" {
		q.Add("_pragma", "query_only(ON)")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func open(path, mode string) (*sql.DB, error) {
	dsn, err := databaseURI(path, mode)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func openBook(path string) (*sql.DB, metadata, error) {
	var meta metadata
	file, err := os.Stat(path)
	if err != nil {
		return nil, meta, err
	}
	if !file.Mode().IsRegular() {
		return nil, meta, errors.New("工具书数据库必须是普通文件")
	}
	db, err := open(path, "ro")
	if err != nil {
		return nil, meta, err
	}
	meta, err = loadMetadata(db)
	if err != nil {
		db.Close()
		return nil, meta, fmt.Errorf("无法读取工具书元数据：%w", err)
	}
	return db, meta, nil
}

func loadMetadata(db *sql.DB) (metadata, error) {
	var m metadata
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return m, err
	}
	if version != 1 {
		return m, fmt.Errorf("工具书数据库版本 %d 不受支持", version)
	}
	if err := checkTable(db, "book_meta"); err != nil {
		return m, err
	}
	const metaSize = `length(CAST(title AS BLOB)) + length(CAST(name AS BLOB)) + length(CAST(description AS BLOB)) +
		length(CAST(content_version AS BLOB)) + length(CAST(updated_at AS BLOB)) + length(CAST(source AS BLOB)) +
		length(CAST(data_table AS BLOB)) + length(CAST(list_fields AS BLOB)) + length(CAST(search_fields AS BLOB)) +
		length(CAST(lookup_keys AS BLOB)) + length(CAST(COALESCE(content_field, '') AS BLOB)) +
		length(CAST(content_format AS BLOB)) + length(CAST(field_options AS BLOB)) +
		length(CAST(order_by AS BLOB)) + length(CAST(help_text AS BLOB))`
	var count, maxSize int
	if err := db.QueryRow("SELECT count(*), coalesce(max("+metaSize+"), 0) FROM book_meta").Scan(&count, &maxSize); err != nil {
		return m, err
	}
	if count != 1 || maxSize > metadataLimit {
		return m, errors.New("book_meta 须恰好包含一条记录，且大小不得超过 256 KiB")
	}
	var id, schema, details int
	var list, search, lookups, options, order string
	err := db.QueryRow(`SELECT id, schema_version, title, name, description, source, data_table, list_fields,
		search_fields, detail_enabled, lookup_keys, COALESCE(content_field, ''), content_format,
		field_options, order_by, help_text FROM book_meta`).Scan(&id, &schema, &m.Title, &m.Name, &m.Description,
		&m.Source, &m.Table, &list, &search, &details, &lookups, &m.Body, &m.ContentFormat, &options, &order, &m.Help)
	if err != nil {
		return m, err
	}
	if id != 1 || schema != version || !bookName.MatchString(m.Name) || (details != 0 && details != 1) {
		return m, errors.New("book_meta 的标识、版本、名称或详情开关无效")
	}
	if m.ContentFormat != "plain" && m.ContentFormat != "markdown" {
		return m, errors.New("不支持该正文格式")
	}
	m.Details = details == 1
	for _, value := range []struct {
		name, json string
		target     any
	}{
		{"list_fields", list, &m.List}, {"search_fields", search, &m.Search},
		{"lookup_keys", lookups, &m.Lookups}, {"field_options", options, &m.Options},
		{"order_by", order, &m.Order},
	} {
		if err := decodeMetadata(value.json, value.target); err != nil {
			return m, fmt.Errorf("%s: %w", value.name, err)
		}
	}
	if err := m.validate(db); err != nil {
		return m, err
	}
	return m, nil
}

func decodeMetadata(text string, target any) error {
	if !json.Valid([]byte(text)) || strings.TrimSpace(text) == "null" {
		return errors.New("需要非 null 的 JSON 值")
	}
	if err := uniqueKeys(json.NewDecoder(strings.NewReader(text)), 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func uniqueKeys(dec *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON 嵌套层级过多")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		keys := make(map[string]bool)
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name := key.(string) // The JSON decoder validates object key tokens.
			if keys[name] {
				return fmt.Errorf("JSON 键 %q 重复", name)
			}
			keys[name] = true
			if err := uniqueKeys(dec, depth+1); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for dec.More() {
			if err := uniqueKeys(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return nil
	}
	_, err = dec.Token()
	return err
}

func checkTable(db *sql.DB, name string) error {
	if !identifier.MatchString(name) {
		return fmt.Errorf("数据表标识 %q 无效", name)
	}
	var kind string
	err := db.QueryRow(`SELECT type FROM pragma_table_list WHERE schema = 'main' AND name = ?`, name).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && kind != "table") {
		return fmt.Errorf("%q 必须是普通数据表", name)
	}
	return err
}

func (m *metadata) validate(db *sql.DB) error {
	if err := checkTable(db, m.Table); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT name, type, pk FROM pragma_table_xinfo(?) WHERE hidden = 0`, m.Table)
	if err != nil {
		return err
	}
	m.fields = make(map[string]string)
	pkCount, idPK := 0, false
	for rows.Next() {
		var name, kind string
		var pk int
		if err := rows.Scan(&name, &kind, &pk); err != nil {
			rows.Close()
			return err
		}
		m.fields[name] = strings.ToUpper(kind)
		if pk != 0 {
			pkCount++
			idPK = idPK || name == "id"
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(m.List) == 0 || len(m.Order) == 0 || m.Order[len(m.Order)-1] != "id" || pkCount != 1 || !idPK {
		return errors.New("工具书须声明列表字段和稳定排序，最后一个排序字段须为主键 id")
	}
	for _, fields := range [][]string{m.List, m.Search, m.Order} {
		if err := m.validateFields(fields); err != nil {
			return err
		}
	}
	for _, field := range m.Search {
		if m.fields[field] != "TEXT" {
			return fmt.Errorf("搜索字段 %q 须为 TEXT 类型", field)
		}
	}
	if m.Details && (!identifier.MatchString(m.Body) || m.fields[m.Body] != "TEXT") {
		return errors.New("详情正文必须存放在 TEXT 字段")
	}
	if !m.Details && len(m.Lookups) != 0 {
		return errors.New("启用详情功能后才能定义查找规则")
	}
	seenOptions, seenNames, seenArity := map[string]bool{}, map[string]bool{}, map[int]bool{}
	for _, rule := range m.Lookups {
		switch rule.Option {
		case "--id", "--name", "--key":
		default:
			return fmt.Errorf("不支持查找选项 %q", rule.Option)
		}
		if rule.Name == "" || seenNames[rule.Name] || seenOptions[rule.Option] || len(rule.Fields) == 0 ||
			(rule.Positional && seenArity[len(rule.Fields)]) {
			return errors.New("查找规则重复或无效")
		}
		seenOptions[rule.Option], seenNames[rule.Name] = true, true
		if rule.Positional {
			seenArity[len(rule.Fields)] = true
		}
		if err := m.validateFields(rule.Fields); err != nil {
			return err
		}
	}
	for field, option := range m.Options {
		if err := m.validateFields([]string{field}); err != nil {
			return err
		}
		if len(option.Substitutions) != 0 && (m.fields[field] != "TEXT" ||
			(option.Format != "" && option.Format != "text" && option.Format != "oid")) {
			return fmt.Errorf("工具书替换仅支持文本字段，字段 %q 不是文本", field)
		}
		for token, variable := range option.Substitutions {
			if !bookToken.MatchString(token) || !envregistry.ValidName(variable) {
				return fmt.Errorf("工具书字段 %q 的替换配置无效", field)
			}
		}
		if option.Width != 0 && (option.Width < 4 || option.Width > 120) {
			return fmt.Errorf("字段 %q 的显示宽度无效", field)
		}
		switch option.Format {
		case "", "text", "integer", "hex8", "oid", "support":
		default:
			return fmt.Errorf("不支持格式 %q", option.Format)
		}
	}
	return nil
}

func (m metadata) validateFields(fields []string) error {
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		_, exists := m.fields[field]
		if seen[field] || !identifier.MatchString(field) || !exists {
			return fmt.Errorf("字段 %q 无效或重复", field)
		}
		seen[field] = true
	}
	return nil
}
