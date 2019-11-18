package redis

import (
	"time"

	"github.com/go-redis/redis"
	"github.com/jpillora/backoff"
	"github.com/seatgeek/resec/resec/state"
	log "github.com/sirupsen/logrus"
	cli "gopkg.in/urfave/cli.v1"
)

func NewConnection(m *cli.Context) (*Manager, error) {
	redisConfig := &Config{
		Address: m.String("redis-addr"),
	}

	instance := &Manager{
		client: redis.NewClient(&redis.Options{
			Addr:         redisConfig.Address,
			DialTimeout:  1 * time.Second,
			Password:     m.String("redis-password"),
			ReadTimeout:  1 * time.Second,
			WriteTimeout: 1 * time.Second,
		}),
		config: redisConfig,
		logger: log.WithFields(log.Fields{
			"system":     "redis",
			"redis_addr": m.String("redis-addr"),
		}),
		state:     &state.Redis{},
		stateCh:   make(chan state.Redis, 10),
		commandCh: make(chan Command, 10),
		stopCh:    make(chan interface{}, 1),
		backoff: &backoff.Backoff{
			Min:    50 * time.Millisecond,
			Max:    10 * time.Second,
			Factor: 1.5,
			Jitter: false,
		},
	}

	return instance, nil
}
