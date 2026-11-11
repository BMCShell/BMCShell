package librarydb

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
)

// Field describes a business column and the constraints applied by record edits.
type Field struct {
	Description string       `json:"description,omitempty"`
	Type        string       `json:"type,omitempty"`
	Required    bool         `json:"required,omitempty"`
	Pattern     string       `json:"pattern,omitempty"`
	Enum        []string     `json:"enum,omitempty"`
	MaxBytes    int          `json:"maxBytes,omitempty"`
	Min         *json.Number `json:"min,omitempty"`
	Max         *json.Number `json:"max,omitempty"`
}

func (field Field) validateDefinition() error {
	switch field.Type {
	case "", "text", "integer", "real", "blob":
	default:
		return fmt.Errorf("不支持字段类型 %q", field.Type)
	}
	if (field.Pattern != "" || len(field.Enum) != 0 || field.MaxBytes != 0) && field.Type != "text" {
		return errors.New("只有文本字段可以使用 pattern、enum 和 maxBytes")
	}
	if field.MaxBytes < 0 {
		return errors.New("maxBytes 须大于或等于 0")
	}
	if field.Pattern != "" {
		if _, err := regexp.Compile(field.Pattern); err != nil {
			return fmt.Errorf("匹配规则 pattern 无效：%w", err)
		}
	}
	if field.Min == nil && field.Max == nil {
		return nil
	}
	if field.Type != "integer" && field.Type != "real" {
		return errors.New("只有数值字段可以使用 min 和 max")
	}
	_, _, err := field.bounds()
	return err
}

func (field Field) bounds() (min, max *big.Float, err error) {
	if min, err = field.parseBound(field.Min); err != nil {
		return nil, nil, fmt.Errorf("最小值 min 无效：%w", err)
	}
	if max, err = field.parseBound(field.Max); err != nil {
		return nil, nil, fmt.Errorf("最大值 max 无效：%w", err)
	}
	if min != nil && max != nil && min.Cmp(max) > 0 {
		return nil, nil, errors.New("min 须小于或等于 max")
	}
	return min, max, nil
}

func (field Field) parseBound(bound *json.Number) (*big.Float, error) {
	if bound == nil {
		return nil, nil
	}
	if field.Type == "integer" {
		value, err := bound.Int64()
		if err != nil {
			return nil, errors.New("须为 64 位有符号整数")
		}
		return new(big.Float).SetInt64(value), nil
	}
	value, err := bound.Float64()
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return nil, errors.New("须为有限数值")
	}
	return big.NewFloat(value), nil
}

func (field Field) validateValue(value any) error {
	if value == nil {
		if field.Required {
			return errors.New("不能为空值（NULL）")
		}
		return nil
	}
	switch field.Type {
	case "text":
		value, ok := value.(string)
		if !ok {
			return errors.New("必须是文本")
		}
		if field.MaxBytes > 0 && len(value) > field.MaxBytes {
			return fmt.Errorf("不能超过 %d 字节", field.MaxBytes)
		}
		if field.Pattern != "" {
			matches, err := regexp.MatchString(field.Pattern, value)
			if err != nil {
				return err
			}
			if !matches {
				return errors.New("格式不符合字段定义")
			}
		}
		if len(field.Enum) > 0 && !slices.Contains(field.Enum, value) {
			return errors.New("值不在允许的选项内")
		}
	case "integer":
		if _, ok := value.(int64); !ok {
			return errors.New("必须是整数")
		}
		return field.validateBounds(value)
	case "real":
		switch number := value.(type) {
		case int64:
		case float64:
			if math.IsInf(number, 0) || math.IsNaN(number) {
				return errors.New("必须是有限数值")
			}
		default:
			return errors.New("必须是数值")
		}
		return field.validateBounds(value)
	case "blob":
		if _, ok := value.([]byte); !ok {
			return errors.New("必须是二进制")
		}
	}
	return nil
}

func (field Field) validateBounds(value any) error {
	if field.Min == nil && field.Max == nil {
		return nil
	}
	min, max, err := field.bounds()
	if err != nil {
		return err
	}
	var number big.Float
	switch value := value.(type) {
	case int64:
		number.SetInt64(value)
	case float64:
		number.SetFloat64(value)
	}
	if min != nil && number.Cmp(min) < 0 {
		return fmt.Errorf("必须大于或等于 %s", *field.Min)
	}
	if max != nil && number.Cmp(max) > 0 {
		return fmt.Errorf("必须小于或等于 %s", *field.Max)
	}
	return nil
}
