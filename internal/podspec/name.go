package podspec

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

const maxK8sNameLen = 63

var notDNS1123 = regexp.MustCompile(`[^a-z0-9-]`)

// SanitizeName converts a Docker container name to a valid RFC 1123 pod name.
// If the input is empty, a random name like "dik-<hex8>" is returned.
func SanitizeName(input string) (string, error) {
	if input == "" {
		return randomName(), nil
	}
	out := strings.ToLower(input)
	out = notDNS1123.ReplaceAllString(out, "-")
	out = collapseDashes(out)
	out = strings.Trim(out, "-")
	if len(out) > maxK8sNameLen {
		out = strings.TrimRight(out[:maxK8sNameLen], "-")
	}
	if out == "" {
		return "", fmt.Errorf("name %q has no valid characters after sanitization", input)
	}
	return out, nil
}

// GeneratedName returns a deterministic-looking but random pod name based on
// the image (e.g. "redis:7" -> "dik-redis-<hex6>"). Used when no --name is
// provided.
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

func collapseDashes(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if r == '-' {
			if prevDash {
				continue
			}
			prevDash = true
		} else {
			prevDash = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// imageBase extracts the image name without registry path or tag/digest.
// "registry.example.com/library/redis:7.2" -> "redis"
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
		// crypto/rand cannot fail on the platforms we support; if it does
		// the process is hosed anyway.
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)[:n]
}
