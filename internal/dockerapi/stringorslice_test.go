package dockerapi_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bpaquet/docker-in-kubernetes/internal/dockerapi"
)

func TestStringOrSliceUnmarshal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want dockerapi.StringOrSlice
	}{
		{"null", "null", nil},
		{"empty string", `""`, nil},
		{"single string", `"redis-server"`, dockerapi.StringOrSlice{"redis-server"}},
		{"array", `["redis-server","--port","6379"]`, dockerapi.StringOrSlice{"redis-server", "--port", "6379"}},
		{"empty array", `[]`, dockerapi.StringOrSlice{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got dockerapi.StringOrSlice
			require.NoError(t, json.Unmarshal([]byte(tc.in), &got))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestStringOrSliceUnmarshalRejectsGarbage(t *testing.T) {
	var got dockerapi.StringOrSlice
	err := json.Unmarshal([]byte(`42`), &got)
	require.Error(t, err)
}

func TestStringOrSliceMarshal(t *testing.T) {
	out, err := json.Marshal(dockerapi.StringOrSlice{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, `["a","b"]`, string(out))

	out, err = json.Marshal(dockerapi.StringOrSlice(nil))
	require.NoError(t, err)
	assert.Equal(t, `null`, string(out))
}
