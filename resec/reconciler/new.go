package reconciler

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/seatgeek/resec/resec/consul"
	"github.com/seatgeek/resec/resec/redis"
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

	reconciler := &Reconciler{
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

	signal.Notify(reconciler.signalCh, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)
	signal.Notify(reconciler.debugSignalCh, syscall.SIGUSR1, syscall.SIGUSR2)

	go redisConnection.CommandRunner()
	go consulConnection.CommandRunner()

	return reconciler, nil
}
