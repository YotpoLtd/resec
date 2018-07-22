package redis

import (
	"fmt"

	"github.com/YotpoLtd/resec/resec/state"
	"github.com/go-redis/redis"
	log "github.com/sirupsen/logrus"
	cli "gopkg.in/urfave/cli.v1"
)

func NewConnection(m *cli.Context) (*Manager, error) {
	redisConfig := &Config{
		Address: m.String("redis-addr"),
	}

	instance := &Manager{
		client: redis.NewClient(&redis.Options{
			Addr:        redisConfig.Address,
			DialTimeout: m.Duration("healthcheck-timeout"),
			Password:    m.String("redis-password"),
			ReadTimeout: m.Duration("healthcheck-timeout"),
		}),
		config:    redisConfig,
		logger:    log.WithField("system", "redis"),
		state:     &state.Redis{},
		stateCh:   make(chan state.Redis, 1),
		commandCh: make(chan Command, 1),
		stopCh:    make(chan interface{}, 1),
	}

	if err := instance.client.Ping().Err(); err != nil {
		return nil, fmt.Errorf("Can't communicate to Redis server: %s", err)
	}

	return instance, nil
}