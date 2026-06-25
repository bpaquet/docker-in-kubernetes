package podspec

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

const maxK8sNameLen = 63

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// SanitizeName converts a Docker name to RFC 1123. Empty input returns a random name.
func SanitizeName(input string) (string, error) {
	if input == "" {
		return randomName(), nil
	}
	out := nonAlnum.ReplaceAllString(strings.ToLower(input), "-")
	out = strings.Trim(out, "-")
	if len(out) > maxK8sNameLen {
		out = strings.TrimRight(out[:maxK8sNameLen], "-")
	}
	if out == "" {
		return "", fmt.Errorf("name %q has no valid characters after sanitization", input)
	}
	return out, nil
}

// GeneratedName returns "dik-<image-base>-<hex6>" for use when --name is empty.
func GeneratedName(image string) string {
	base := imageBase(image)
	clean, err := SanitizeName("dik-" + base)
	if err != nil || clean == "" {
		clean = "dik"
	}
	suffix := randomSuffix(6)
	full := clean + "-" + suffix
	if len(full) > maxK8sNameLen {
		full = full[:maxK8sNameLen]
	}
	return full
}

// imageBase: "registry.example.com/library/redis:7.2" -> "redis".
func imageBase(image string) string {
	s := image
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, ":@"); i >= 0 {
		s = s[:i]
	}
	return s
}

func randomName() string {
	return "dik-" + randomSuffix(8)
}

func randomSuffix(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)[:n]
}
