package resec

import (
	"os"
	"os/signal"
	"syscall"

	cli "gopkg.in/urfave/cli.v1"
)

// setup returns the default configuration for the ReSeC
func setup(c *cli.Context) (*reconciler, error) {
	redisConnection, err := newRedisConnection(c)
	if err != nil {
		return nil, err
	}

	consulConnection, err := newConsulConnection(c, redisConnection.config)
	if err != nil {
		return nil, err
	}

	reconsiler := &reconciler{
		consulCommandCh:   consulConnection.commandCh,
		consulUpdateCh:    consulConnection.stateCh,
		reconsileInterval: c.Duration("healthcheck-timeout"),
		redisCommand:      redisConnection.commandCh,
		redisUpdateCh:     redisConnection.stateCh,
		signalCh:          make(chan os.Signal, 1),
		stopCh:            make(chan interface{}, 1),
	}

	signal.Notify(reconsiler.signalCh, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)
	go redisConnection.commandRunner()
	go consulConnection.commandRunner()

	return reconsiler, nil
}
