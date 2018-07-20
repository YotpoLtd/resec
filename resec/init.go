package resec

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/YotpoLtd/resec/resec/consul"
	"github.com/YotpoLtd/resec/resec/redis"
	cli "gopkg.in/urfave/cli.v1"
)

// setup returns the default configuration for the ReSeC
func setup(c *cli.Context) (*reconciler, error) {
	redisConnection, err := redis.NewConnection(c)
	if err != nil {
		return nil, err
	}

	consulConnection, err := consul.NewConnection(c, redisConnection.Config())
	if err != nil {
		return nil, err
	}

	reconsiler := &reconciler{
		consulCommandCh:   consulConnection.CommandCh,
		consulUpdateCh:    consulConnection.StateCh,
		reconsileInterval: c.Duration("healthcheck-timeout"),
		redisCommand:      redisConnection.CommandCh,
		redisUpdateCh:     redisConnection.StateCh,
		signalCh:          make(chan os.Signal, 1),
		stopCh:            make(chan interface{}, 1),
	}

	signal.Notify(reconsiler.signalCh, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)
	go redisConnection.CommandRunner()
	go consulConnection.CommandRunner()

	return reconsiler, nil
}
