package internal

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Модели возвращают JSON вольно: вес то числом 23000, то строкой "23000",
// то вовсе "23 tonna"; телефон то строкой, то числом. Строгие типы на этом
// падают и теряется вся пачка объявлений. Поэтому разбираем гибко.

// FlexFloat — число, которое может прийти строкой или быть пустым.
type FlexFloat struct {
	Value *float64
}

func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" || s == `""` {
		return nil
	}
	// обычное число
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		f.Value = &v
		return nil
	}
	// строка: "23000", "23 tonna", "23,5"
	var str string
	if err := json.Unmarshal(b, &str); err != nil {
		return nil // не смогли — считаем, что веса нет
	}
	str = strings.TrimSpace(str)
	if str == "" {
		return nil
	}
	// оставляем цифры и разделитель дробной части
	var digits strings.Builder
	for _, r := range str {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case (r == '.' || r == ',') && digits.Len() > 0:
			digits.WriteRune('.')
		}
	}
	d := strings.TrimSuffix(digits.String(), ".")
	if d == "" {
		return nil
	}
	if v, err := strconv.ParseFloat(d, 64); err == nil {
		f.Value = &v
	}
	return nil
}

// FlexString — строка, которая может прийти числом (например телефон).
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*f = FlexString(str)
		return nil
	}
	// число или что-то ещё — берём как есть, без кавычек
	*f = FlexString(strings.Trim(s, `"`))
	return nil
}

func (f FlexString) String() string { return string(f) }
