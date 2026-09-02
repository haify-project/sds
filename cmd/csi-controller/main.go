package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/liliang-cn/sds/pkg/client"
	"github.com/liliang-cn/sds/pkg/csi"
	"go.uber.org/zap"
)

func main() {
	endpoint := flag.String("endpoint", "unix:///csi/csi.sock", "CSI gRPC endpoint")
	sdsAddr := flag.String("sds-controller", "sds-controller:3374", "sds-controller gRPC address")
	flag.Parse()

	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	sds, err := client.NewSDSClient(*sdsAddr)
	if err != nil {
		log.Fatal("connect sds-controller", zap.Error(err))
	}
	// Runs on an orderly shutdown, once the driver has stopped serving. All this
	// can report is that tearing down an idle gRPC transport was untidy, on a
	// process that is about to exit and has nothing left to retry, so the error
	// is dropped rather than logged as a shutdown failure.
	defer func() { _ = sds.Close() }()

	d := csi.NewDriver(*endpoint, log,
		csi.NewIdentityServer(),
		csi.NewControllerServer(sds, log),
		nil,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil {
		log.Error("driver exited", zap.Error(err))
		os.Exit(1)
	}
}
