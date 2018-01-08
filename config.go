package main

import (
	"fmt"
	"github.com/go-redis/redis"
	consulapi "github.com/hashicorp/consul/api"
	"log"
	"os"
	"strconv"
	"strings"
)

const (
	ConsulServiceName = "CONSUL_SERVICE_NAME"
	ConsulLockKey     = "CONSUL_LOCK_KEY"
	RedisAddr         = "REDIS_ADDR"
)

type resecConfig struct {
	consulClientConfig *consulapi.Config
	consulServiceName  string
	consulLockKey      string
	redisAddr          string
	redisHost          string
	redisPort          int
	masterCh           chan []*consulapi.ServiceEntry
	stopCh             chan struct{}
	stopWatchCh        chan struct{}
	errCh              chan error
	lockCh             chan *consulapi.Lock
	leaderCh           chan struct{}
	lockAbortCh        chan struct{}
	sessionId          string
}

// defaultConfig returns the default configuration for the ReSeC
func defaultConfig() *resecConfig {
	config := &resecConfig{
		consulClientConfig: consulapi.DefaultConfig(),
		consulServiceName:  "redis",
		consulLockKey:      "resec/.lock",
		redisAddr:          "127.0.0.1:6379",
		masterCh:           make(chan []*consulapi.ServiceEntry),
		stopCh:             make(chan struct{}),
		stopWatchCh:        make(chan struct{}),
	}

	if consulServiceName := os.Getenv(ConsulServiceName); consulServiceName != "" {
		config.consulServiceName = consulServiceName
	}

	if consulLockKey := os.Getenv(ConsulLockKey); consulLockKey != "" {
		config.consulServiceName = consulLockKey
	}

	if redisAddr := os.Getenv(RedisAddr); redisAddr != "" {
		redisHost := strings.Split(redisAddr, ":")[0]
		redisPort, err := strconv.Atoi(strings.Split(redisAddr, ":")[1])
		if err != nil {
			fmt.Println("Cannot convert to int REDIS_PORT, %s", err)
		}
		config.redisAddr = redisAddr
		config.redisHost = redisHost
		config.redisPort = redisPort
	}

	return config
}

func (rc *resecConfig) consulClient() *consulapi.Client {
	consulClient, err := consulapi.NewClient(rc.consulClientConfig)

	if err != nil {
		log.Fatalf("Can't connect to Consul on %s", rc.consulClientConfig.Address)
	}

	return consulClient
}

func (rc *resecConfig) redisClient() *redis.Client {

	redisClient := redis.NewClient(&redis.Options{
		Addr: rc.redisAddr,
	})

	_, err := redisClient.Ping().Result()

	if err != nil {
		log.Fatalf("Can't Connect to redis running on %s", rc.redisAddr)
	}

	return redisClient
}
