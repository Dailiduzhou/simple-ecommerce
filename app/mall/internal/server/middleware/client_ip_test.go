package middleware

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientIPResolverOnlyTrustsForwardedHeadersBehindAKnownProxy(t *testing.T) {
	resolver, err := NewClientIPResolver([]string{"10.0.0.0/8", "2001:db8::/32", "192.168.1.5"})
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		peer string
		xff  string
		want string
	}{
		{"untrusted peer ignores xff", "198.51.100.9:5555", "203.0.113.7", "198.51.100.9"},
		{"trusted peer single hop", "10.1.2.3:5555", "203.0.113.7", "203.0.113.7"},
		{"trusted peer skips trusted hops", "10.1.2.3:5555", "203.0.113.7, 10.9.9.9", "203.0.113.7"},
		{"missing header falls back to peer", "10.1.2.3:5555", "", "10.1.2.3"},
		{"malformed header falls back to peer", "10.1.2.3:5555", "attacker, bogus", "10.1.2.3"},
		{"ipv6 trusted peer", "[2001:db8::1]:5555", "203.0.113.7", "203.0.113.7"},
		{"bare ip trusted proxy", "192.168.1.5:80", "203.0.113.7", "203.0.113.7"},
		{"every hop trusted falls back to peer", "10.1.2.3:5555", "10.1.1.1, 10.2.2.2", "10.1.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{}
			if tc.xff != "" {
				header.Set("X-Forwarded-For", tc.xff)
			}
			require.Equal(t, tc.want, resolver.ClientIP(tc.peer, header))
		})
	}
}

func TestClientIPResolverDefaultsToThePeerAddress(t *testing.T) {
	resolver, err := NewClientIPResolver(nil)
	require.NoError(t, err)
	require.NotNil(t, resolver)

	header := http.Header{"X-Forwarded-For": {"203.0.113.7"}}
	require.Equal(t, "198.51.100.9", resolver.ClientIP("198.51.100.9:5555", header))
	// A nil resolver (gRPC, invalid configuration) never trusts any hop.
	var none *ClientIPResolver
	require.Equal(t, "198.51.100.9", none.ClientIP("198.51.100.9:5555", header))
}

func TestNewClientIPResolverRejectsInvalidEntries(t *testing.T) {
	for _, entry := range []string{"not-an-ip", "10.0.0.0/33", "10.0.0.0/abc"} {
		_, err := NewClientIPResolver([]string{entry})
		require.Errorf(t, err, "entry %q", entry)
	}
}
