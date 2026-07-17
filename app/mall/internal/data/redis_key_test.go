package data

import "testing"

func TestRedisKey(t *testing.T) {
	tests := []struct {
		name  string
		parts []any
		want  string
	}{
		{name: "empty", want: ""},
		{name: "mixed parts", parts: []any{"order", "user", int64(42), int32(20), 0}, want: "order:user:42:20:0"},
		{name: "part containing separator", parts: []any{"payment", "order", 7, "active", "wechat:native"}, want: "payment:order:7:active:wechat:native"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redisKey(tt.parts...); got != tt.want {
				t.Fatalf("redisKey() = %q, want %q", got, tt.want)
			}
		})
	}
}
