package polymarket

import (
	"encoding/json"
	"testing"
)

func TestFlexFloat(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{`0.42`, 0.42},
		{`"0.42"`, 0.42},
		{`null`, 0},
		{`""`, 0},
		{`12345`, 12345},
		{`"1000000000.123"`, 1000000000.123},
	}
	for _, c := range cases {
		var f FlexFloat
		if err := json.Unmarshal([]byte(c.in), &f); err != nil {
			t.Fatalf("unmarshal %q: %v", c.in, err)
		}
		if f.Float64() != c.want {
			t.Fatalf("%q => %v want %v", c.in, f.Float64(), c.want)
		}
	}
}

func TestFlexInt(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{`42`, 42},
		{`"42"`, 42},
		{`null`, 0},
		{`""`, 0},
		{`42.0`, 42},
	}
	for _, c := range cases {
		var f FlexInt
		if err := json.Unmarshal([]byte(c.in), &f); err != nil {
			t.Fatalf("unmarshal %q: %v", c.in, err)
		}
		if f.Int64() != c.want {
			t.Fatalf("%q => %v want %v", c.in, f.Int64(), c.want)
		}
	}
}

func TestFlexStringSlice(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`["a","b"]`, []string{"a", "b"}},
		{`"[\"a\",\"b\"]"`, []string{"a", "b"}},
		{`null`, nil},
		{`""`, nil},
		{`[]`, []string{}},
	}
	for _, c := range cases {
		var s FlexStringSlice
		if err := json.Unmarshal([]byte(c.in), &s); err != nil {
			t.Fatalf("unmarshal %q: %v", c.in, err)
		}
		if len(s) != len(c.want) {
			t.Fatalf("%q => %v want %v", c.in, s, c.want)
		}
		for i := range s {
			if s[i] != c.want[i] {
				t.Fatalf("%q => %v want %v", c.in, s, c.want)
			}
		}
	}
}
