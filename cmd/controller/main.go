package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/liliang-cn/sds/pkg/config"
	"github.com/liliang-cn/sds/pkg/controller"
)

var (
	version = "dev"
)

func main() {
	configPath := flag.String("config", "", "Path to configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	logger, err := initLogger(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	logger.Info("Starting SDS controller",
		zap.String("version", version),
		zap.String("config", *configPath),
		zap.Bool("metrics_enabled", cfg.Metrics.Enabled),
		zap.String("metrics_address", fmt.Sprintf("%s:%d", cfg.Metrics.ListenAddress, cfg.Metrics.Port)))

	// Create controller
	ctrl, err := controller.New(cfg, logger)
	if err != nil {
		logger.Fatal("Failed to create controller", zap.Error(err))
	}

	// Start controller
	if err := ctrl.Start(); err != nil {
		logger.Fatal("Failed to start controller", zap.Error(err))
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down...")
	ctrl.Stop()
	logger.Info("Shutdown complete")
}

// initLogger initializes the logger
func initLogger(cfg *config.Config) (*zap.Logger, error) {
	var zapConfig zap.Config

	if cfg.Log.Format == "json" {
		zapConfig = zap.NewProductionConfig()
	} else {
		zapConfig = zap.NewDevelopmentConfig()
	}

	// Set log level
	switch cfg.Log.Level {
	case "debug":
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	case "info":
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	case "warn":
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.WarnLevel)
	case "error":
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.ErrorLevel)
	default:
		zapConfig.Level = zap.NewAtomicLevelAt(zapcore.InfoLevel)
	}

	return zapConfig.Build()
}
