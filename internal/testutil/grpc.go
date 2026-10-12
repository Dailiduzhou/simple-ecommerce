package testutil

import (
	"crypto/tls"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ServeGRPC starts a loopback receiver after registering its protocol services.
func ServeGRPC(t testing.TB, tlsEnabled bool, register func(*grpc.Server)) (addr, caFile string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	var opts []grpc.ServerOption
	if tlsEnabled {
		var cert tls.Certificate
		cert, caFile = SelfSignedCertificate(t)
		opts = append(opts, grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}})))
	}
	server := grpc.NewServer(opts...)
	register(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String(), caFile
}
