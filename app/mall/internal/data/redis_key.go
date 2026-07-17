package data

import (
	"fmt"
	"strings"
)

// redisKey builds a colon-delimited Redis key from its logical parts.
func redisKey(parts ...any) string {
	var key strings.Builder
	for i, part := range parts {
		if i > 0 {
			key.WriteByte(':')
		}
		_, _ = fmt.Fprint(&key, part)
	}
	return key.String()
}
