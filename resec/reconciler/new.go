package reconciler

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/YotpoLtd/resec/resec/consul"
	"github.com/YotpoLtd/resec/resec/redis"
	cli "gopkg.in/urfave/cli.v1"
)

// setup returns the default configuration for the ReSeC
func NewReconciler(c *cli.Context) (*Reconciler, error) {
	redisConnection, err := redis.NewConnection(c)
	if err != nil {
		return nil, err
	}

	consulConnection, err := consul.NewConnection(c, redisConnection.Config())
	if err != nil {
		return nil, err
	}

	reconsiler := &Reconciler{
		consulCommandCh:        consulConnection.GetCommandWriter(),
		consulStateCh:          consulConnection.GetStateReader(),
		debugSignalCh:          make(chan os.Signal, 1),
		forceReconcileInterval: c.Duration("healthcheck-timeout"),
		reconcileCh:            make(chan interface{}, 10),
		redisCommandCh:         redisConnection.CommandChWriter(),
		redisStateCh:           redisConnection.StateChReader(),
		signalCh:               make(chan os.Signal, 1),
		stopCh:                 make(chan interface{}, 1),
	}

	signal.Notify(reconsiler.signalCh, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)
	signal.Notify(reconsiler.debugSignalCh, syscall.SIGUSR1, syscall.SIGUSR2)

	go redisConnection.CommandRunner()
	go consulConnection.CommandRunner()

	return reconsiler, nil
}
