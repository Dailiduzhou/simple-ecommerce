package data

import (
	"testing"
	"time"

	"github.com/Dailiduzhou/simple-ecommerce/app/mall/internal/conf"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestCacheWorkTimeout_Config(t *testing.T) {
	for _, tc := range []struct {
		name string
		conf *conf.Data
		want time.Duration
	}{
		{name: "nil_config", want: 3 * time.Second},
		{name: "no_cache_config", conf: &conf.Data{}, want: 3 * time.Second},
		{name: "unset", conf: &conf.Data{Cache: &conf.Data_Cache{}}, want: 3 * time.Second},
		{name: "zero", conf: &conf.Data{Cache: &conf.Data_Cache{SharedWorkTimeout: durationpb.New(0)}}, want: 3 * time.Second},
		{name: "negative", conf: &conf.Data{Cache: &conf.Data_Cache{SharedWorkTimeout: durationpb.New(-time.Second)}}, want: 3 * time.Second},
		{name: "positive", conf: &conf.Data{Cache: &conf.Data_Cache{SharedWorkTimeout: durationpb.New(125 * time.Millisecond)}}, want: 125 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// NewData does not access PostgreSQL/Redis at construction time.
			// No resources were allocated, so its resource cleanup is not needed.
			d, _, err := NewData(tc.conf, nil, nil)
			require.NoError(t, err)
			require.Equal(t, tc.want, d.sharedCacheWorkTimeout)
			require.Equal(t, tc.want, d.sharedCacheTimeout())
		})
	}
}

func TestCacheWorkTimeout_DirectConstruction(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second, 50 * time.Millisecond} {
		d := &Data{sharedCacheWorkTimeout: timeout}
		want := timeout
		if timeout <= 0 {
			want = 3 * time.Second
		}
		require.Equal(t, want, d.sharedCacheTimeout())
	}
}
