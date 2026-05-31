package polymarket

import (
	"encoding/json"
	"strconv"
)

// FlexFloat decodes either a JSON number or a numeric string.
// Polymarket APIs return numeric fields inconsistently (e.g. "0.1745" string
// from CLOB midpoint vs 0.1745 number from Data API trades). Use this for
// every numeric field that has been observed as either form.
type FlexFloat float64

func (f *FlexFloat) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	// raw number
	if b[0] != '"' {
		var v float64
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*f = FlexFloat(v)
		return nil
	}
	// quoted string
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = FlexFloat(v)
	return nil
}

func (f FlexFloat) Float64() float64 { return float64(f) }

// FlexInt mirrors FlexFloat for integer fields.
type FlexInt int64

func (i *FlexInt) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*i = 0
		return nil
	}
	if b[0] != '"' {
		var v int64
		if err := json.Unmarshal(b, &v); err != nil {
			// fallback: maybe float
			var fv float64
			if err2 := json.Unmarshal(b, &fv); err2 == nil {
				*i = FlexInt(int64(fv))
				return nil
			}
			return err
		}
		*i = FlexInt(v)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		*i = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*i = FlexInt(v)
	return nil
}

func (i FlexInt) Int() int     { return int(i) }
func (i FlexInt) Int64() int64 { return int64(i) }

// FlexStringSlice decodes either:
//   - a JSON array of strings: ["a","b"]
//   - a JSON-encoded string holding an array: "[\"a\",\"b\"]"
//
// Gamma API returns clobTokenIds/outcomes as JSON-encoded strings.
type FlexStringSlice []string

func (s *FlexStringSlice) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*s = nil
		return nil
	}
	// raw array
	if b[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return err
		}
		*s = arr
		return nil
	}
	// quoted string containing JSON
	var raw string
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == "" {
		*s = nil
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return err
	}
	*s = arr
	return nil
}
