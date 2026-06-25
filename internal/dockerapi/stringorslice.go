package dockerapi

import (
	"encoding/json"
	"fmt"
)

// StringOrSlice decodes a JSON field that may be a string OR a []string (Cmd, Entrypoint).
type StringOrSlice []string

// UnmarshalJSON accepts a string or a []string.
func (s *StringOrSlice) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*s = nil
		return nil
	}
	if data[0] == '[' {
		var arr []string
		if err := json.Unmarshal(data, &arr); err != nil {
			return fmt.Errorf("StringOrSlice: %w", err)
		}
		*s = arr
		return nil
	}
	if data[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return fmt.Errorf("StringOrSlice: %w", err)
		}
		if str == "" {
			*s = nil
			return nil
		}
		*s = []string{str}
		return nil
	}
	return fmt.Errorf("StringOrSlice: unexpected JSON value %s", data)
}

// MarshalJSON emits []string, or null when empty.
func (s StringOrSlice) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return []byte("null"), nil
	}
	return json.Marshal([]string(s))
}
