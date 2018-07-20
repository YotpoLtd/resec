package resec

import (
	"os"
	"os/signal"
	"syscall"

	log "github.com/sirupsen/logrus"
	cli "gopkg.in/urfave/cli.v1"
)

// setup returns the default configuration for the ReSeC
func Setup(c *cli.Context) (*reconciler, error) {
	redisConnection, err := newRedisConnection(c)
	if err != nil {
		log.Fatal(err)
	}

	consulConnection, err := newConsulConnection(c, redisConnection.config)
	if err != nil {
		log.Fatal(err)
	}

	reconsiler := &reconciler{
		consulConnection: consulConnection,
		redisConnection:  redisConnection,
		sigCh:            make(chan os.Signal, 1),
		stopCh:           make(chan interface{}, 1),
	}

	signal.Notify(reconsiler.sigCh, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)
	return reconsiler, nil
}
