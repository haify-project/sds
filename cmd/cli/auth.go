package main

import (
	"github.com/liliang-cn/sds/pkg/client"
)

// newSDSClient builds the controller client with the resolved API token.
// Every command must use this instead of client.NewSDSClient directly.
// Token resolution order is documented on client.ResolveToken:
// --token flag > SDS_TOKEN env > ~/.sds/token > /etc/sds/token.
func newSDSClient() (*client.SDSClient, error) {
	opts := []client.Option{}
	if token := client.ResolveToken(tokenFlag); token != "" {
		opts = append(opts, client.WithToken(token))
	}
	return client.NewSDSClient(controllerAddr, opts...)
}
