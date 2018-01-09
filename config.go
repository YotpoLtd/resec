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
	AnnounceAddr        = "ANNOUNCE_ADDR"
	ConsulServiceName   = "CONSUL_SERVICE_NAME"
	ConsulLockKey       = "CONSUL_LOCK_KEY"
	ConsulCheckInterval = "CONSUL_CHECK_INTERVAL"
	ConsulCheckTimeout  = "CONSUL_CHECK_TIMEOUT"
	RedisAddr           = "REDIS_ADDR"
)

type resecConfig struct {
	announceAddr        string
	announceHost        string
	announcePort        int
	consulClientConfig  *consulapi.Config
	consulServiceName   string
	consulLockKey       string
	consulCheckInterval string
	consulCheckTimeout  string
	redisAddr           string
	masterCh            chan []*consulapi.ServiceEntry
	stopCh              chan struct{}
	stopWatchCh         chan struct{}
	errCh               chan error
	lockCh              <-chan struct{}
	leaderCh            chan struct{}
	lockAbortCh         chan struct{}
	sessionId           string
}

// defaultConfig returns the default configuration for the ReSeC
func defaultConfig() *resecConfig {
	config := &resecConfig{
		consulClientConfig:  consulapi.DefaultConfig(),
		consulServiceName:   "redis",
		consulLockKey:       "resec/.lock",
		consulCheckInterval: "5s",
		consulCheckTimeout:  "2s",
		redisAddr:           "127.0.0.1:6379",
		masterCh:            make(chan []*consulapi.ServiceEntry),
		stopCh:              make(chan struct{}),
		stopWatchCh:         make(chan struct{}),
	}

	if consulServiceName := os.Getenv(ConsulServiceName); consulServiceName != "" {
		config.consulServiceName = consulServiceName
	}

	if consulLockKey := os.Getenv(ConsulLockKey); consulLockKey != "" {
		config.consulLockKey = consulLockKey
	}

	if consulCheckInterval := os.Getenv(ConsulCheckInterval); consulCheckInterval != "" {
		config.consulCheckInterval = consulCheckInterval
	}

	if consulCheckTimeout := os.Getenv(ConsulCheckTimeout); consulCheckTimeout != "" {
		config.consulCheckTimeout = consulCheckTimeout
	}

	if redisAddr := os.Getenv(RedisAddr); redisAddr != "" {
		config.redisAddr = redisAddr
	}

	// If CONSUL_ANNOUNCE_ADDRESS is set it will be used for registration in consul
	// otherwise if redis address is provided - it will be used for registration in consul
	// if redis address is not provided we'll try to determine local private IP and announce it

	if announceAddr := os.Getenv(AnnounceAddr); announceAddr != "" {
		config.announceAddr = announceAddr
	} else {
		redisHost:=  strings.Split(config.redisAddr, ":")[0]
		redisPort:=  strings.Split(config.redisAddr, ":")[1]
		if redisHost != "127.0.0.1" {
			config.announceAddr = config.redisAddr
		} else {
			config.announceAddr = ":" + redisPort
		}
	}

	announceHost := strings.Split(config.announceAddr, ":")[0]
	announcePort, err := strconv.Atoi(strings.Split(config.announceAddr, ":")[1])
	if err != nil {
		fmt.Println("Trouble parsing port number from [%s]", config.redisAddr)
	}
	config.announceHost = announceHost
	config.announcePort = announcePort

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
