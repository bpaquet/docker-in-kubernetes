package dockerapi

import (
	"encoding/json"
	"fmt"
)

// StringOrSlice handles Docker fields that the CLI may send as either a JSON
// string or a JSON array of strings (Cmd, Entrypoint).
type StringOrSlice []string

// UnmarshalJSON decodes a Docker field that may be either a string or a []string.
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

// MarshalJSON emits the slice (or null for an empty one).
func (s StringOrSlice) MarshalJSON() ([]byte, error) {
	if len(s) == 0 {
		return []byte("null"), nil
	}
	return json.Marshal([]string(s))
}
