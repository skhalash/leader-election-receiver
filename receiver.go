// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package leaderreceivercreator

import (
	"fmt"
	"context"
	"os"
	"path/filepath"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var _ receiver.Metrics = (*leaderReceiverCreator)(nil)

// leaderReceiverCreator implements consumer.Metrics.
type leaderReceiverCreator struct {
	params              receiver.CreateSettings
	cfg                 *Config
	nextLogsConsumer    consumer.Logs
	nextMetricsConsumer consumer.Metrics
	nextTracesConsumer  consumer.Traces

	host              component.Host
	subReceiverRunner *receiverRunner
	cancel            context.CancelFunc
}

func newLeaderReceiverCreator(params receiver.CreateSettings, cfg *Config) component.Component {
	return &leaderReceiverCreator{
		params: params,
		cfg:    cfg,
	}
}

// Start receiver_creator.
func (ler *leaderReceiverCreator) Start(ctx context.Context, host component.Host) error {
	ler.host = host
	ctx = context.Background()
	ctx, ler.cancel = context.WithCancel(ctx)

	ler.params.TelemetrySettings.Logger.Info("Starting leader election receiver...")

	client, err := ler.newClient()
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	ler.params.TelemetrySettings.Logger.Info("Creating leader elector...")

	leaderElector, err := newLeaderElector(
		client,
		func(ctx context.Context) {
			ler.params.TelemetrySettings.Logger.Info("Elected as leader")
			if err := ler.startSubReceiver(); err != nil {
				ler.params.TelemetrySettings.Logger.Error("Failed to start subreceiver", zap.Error(err))
			}
		},
		func() {
			ler.params.TelemetrySettings.Logger.Info("Lost leadership")
			if err := ler.stopSubReceiver(); err != nil {
				ler.params.TelemetrySettings.Logger.Error("Failed to stop subreceiver", zap.Error(err))
			}
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create leader elector: %w", err)
	}

	leaderElector.Run(ctx)
	return nil
}

func (ler *leaderReceiverCreator) newClient() (kubernetes.Interface, error) {
	kubeConfigPath := filepath.Join(os.Getenv("HOME"), ".kube/config")

	config, err := rest.InClusterConfig()
	if err != nil {
		ler.params.TelemetrySettings.Logger.Warn("Cannot find in cluster config", zap.Error(err))
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfigPath)
		if err != nil {
			ler.params.TelemetrySettings.Logger.Error("Cannot build ClientConfig", zap.Error(err))
			return nil, err
		}
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		ler.params.TelemetrySettings.Logger.Error("Cannot create Kubernetes client", zap.Error(err))
		return nil, err
	}
	return client, nil
}

func (ler *leaderReceiverCreator) startSubReceiver() error {
	ler.params.TelemetrySettings.Logger.Info("Starting subreceiver",
		zap.String("name", ler.cfg.subreceiverConfig.id.String()))

	ler.subReceiverRunner = newReceiverRunner(ler.params, ler.host)
	if err := ler.subReceiverRunner.start(
		receiverConfig{
			id:     ler.cfg.subreceiverConfig.id,
			config: ler.cfg.subreceiverConfig.config,
		},
		ler.nextLogsConsumer,
		ler.nextMetricsConsumer,
		ler.nextTracesConsumer,
	); err != nil {
		return fmt.Errorf("failed to start subreceiver %s: %w", ler.cfg.subreceiverConfig.id.String(), err)
	}
	return nil
}

func (ler *leaderReceiverCreator) stopSubReceiver() error {
	ler.params.TelemetrySettings.Logger.Info("Stopping subreceiver",
		zap.String("name", ler.cfg.subreceiverConfig.id.String()))

	ler.subReceiverRunner.shutdown(context.Background())
	return nil
}

// Shutdown stops the receiver_creator and all its receivers started at runtime.
func (ler *leaderReceiverCreator) Shutdown(context.Context) error {
	ler.cancel()
	return nil
}
