package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/haify-project/haify/pkg/client"
	"github.com/haify-project/haify/pkg/csi"
	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
)

func main() {
	endpoint := flag.String("endpoint", "unix:///csi/csi.sock", "CSI gRPC endpoint")
	haifyAddr := flag.String("haify-controller", "haify-controller:3374", "haify-controller gRPC address")
	healthInterval := flag.Duration("health-interval", time.Minute,
		"how often to check each volume's replicas and post VolumeDegraded/VolumeRecovered events on its PVC; 0 disables")
	flag.Parse()

	log, _ := zap.NewProduction()
	defer func() { _ = log.Sync() }()

	// With [auth] or [rbac] on the controller, every call needs a token:
	// HAIFY_TOKEN (the manifests fill it from the haify-api-token Secret),
	// else ~/.haify/token or /etc/haify/token.
	haify, err := client.NewHaifyClient(*haifyAddr, client.WithToken(client.ResolveToken("")))
	if err != nil {
		log.Fatal("connect haify-controller", zap.Error(err))
	}
	// Runs on an orderly shutdown, once the driver has stopped serving. All this
	// can report is that tearing down an idle gRPC transport was untidy, on a
	// process that is about to exit and has nothing left to retry, so the error
	// is dropped rather than logged as a shutdown failure.
	defer func() { _ = haify.Close() }()

	d := csi.NewDriver(*endpoint, log,
		csi.NewIdentityServer(),
		csi.NewControllerServer(haify, log),
		nil,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *healthInterval > 0 {
		startHealthReporter(ctx, haify, *healthInterval, log)
	}

	if err := d.Run(ctx); err != nil {
		log.Error("driver exited", zap.Error(err))
		os.Exit(1)
	}
}

// startHealthReporter posts replica health on PVCs. It needs the Kubernetes API,
// so outside a cluster it logs why it is off and the driver runs without it —
// provisioning does not depend on it.
func startHealthReporter(ctx context.Context, haify csi.HaifyBackend, interval time.Duration, log *zap.Logger) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Warn("volume health reporting disabled: not running in a cluster", zap.Error(err))
		return
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Warn("volume health reporting disabled: kubernetes client", zap.Error(err))
		return
	}
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kube.CoreV1().Events("")})
	recorder := broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: csi.DriverName})
	go func() {
		<-ctx.Done()
		broadcaster.Shutdown()
	}()
	go csi.NewHealthReporter(haify, kube, recorder, interval, log).Run(ctx)
	log.Info("volume health reporting enabled", zap.Duration("interval", interval))
}
