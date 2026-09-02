package main

import (
	"io"

	"github.com/liliang-cn/sds/pkg/client"
)

// newSDSClient builds the controller client with the resolved API token and
// transport security. Every command must use this instead of
// client.NewSDSClient directly — a command that dials on its own gets neither
// the token nor TLS, and against a TLS controller it fails at the handshake
// with an error that looks like the controller is down.
//
// Token resolution order is documented on client.ResolveToken:
// --token flag > SDS_TOKEN env > ~/.sds/token > /etc/sds/token.
// TLS follows the same flag-beats-environment rule, see client.ResolveTLS.
func newSDSClient() (*client.SDSClient, error) {
	opts := []client.Option{}
	if token := client.ResolveToken(tokenFlag); token != "" {
		opts = append(opts, client.WithToken(token))
	}
	if tlsOpts := client.ResolveTLS(tlsFlags); tlsOpts.Active() {
		opts = append(opts, client.WithTLS(tlsOpts))
	}
	return client.NewSDSClient(controllerAddr, opts...)
}

// closeClient releases a controller connection at the end of a CLI command.
// The close error is deliberately dropped: it can only report that the
// transport teardown was untidy, which happens after the RPC has already
// returned and its result has been printed. Surfacing it would turn a command
// that did exactly what was asked into a non-zero exit, and there is nothing
// an operator could do about it anyway. Errors that mean the operation itself
// failed all come back from the RPC call, not from Close.
func closeClient(c io.Closer) {
	_ = c.Close()
}
