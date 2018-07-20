package reconciler

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/YotpoLtd/resec/resec/consul"
	"github.com/YotpoLtd/resec/resec/redis"
	cli "gopkg.in/urfave/cli.v1"
)

// setup returns the default configuration for the ReSeC
func Setup(c *cli.Context) (*Reconciler, error) {
	redisConnection, err := redis.NewConnection(c)
	if err != nil {
		return nil, err
	}

	consulConnection, err := consul.NewConnection(c, redisConnection.Config())
	if err != nil {
		return nil, err
	}

	reconsiler := &Reconciler{
		consulCommandCh:        consulConnection.CommandChWriter(),
		consulStateCh:          consulConnection.StateChReader(),
		forceReconcileInterval: c.Duration("healthcheck-timeout"),
		reconcileInterval:      100 * time.Millisecond,
		redisCommandCh:         redisConnection.CommandChWriter(),
		redisStateCh:           redisConnection.StateChReader(),
		signalCh:               make(chan os.Signal, 1),
		stopCh:                 make(chan interface{}, 1),
	}

	signal.Notify(reconsiler.signalCh, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)
	go redisConnection.CommandRunner()
	go consulConnection.CommandRunner()

	return reconsiler, nil
}
