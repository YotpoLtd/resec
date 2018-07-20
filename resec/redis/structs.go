package redis

import (
	"github.com/YotpoLtd/resec/resec/state"
	"github.com/go-redis/redis"
	log "github.com/sirupsen/logrus"
)

type Connection struct {
	logger    *log.Entry       // logging specificall for Redis
	client    *redis.Client    // redis client
	config    *RedisConfig     // redis config
	state     *state.Redis     // redis state
	StateCh   chan state.Redis // redis state channel to publish updates to the reconciler
	CommandCh chan Command
	stopCh    chan interface{}
}

type RedisConfig struct {
	Address string // address (IP+Port) used to talk to Redis
}
