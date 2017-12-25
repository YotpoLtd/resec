package main

import (
	"fmt"
	consulapi "github.com/hashicorp/consul/api"
	"os"
	"strconv"
	"strings"
)

const (
	ConsulSericeName = "CONSUL_SERVICE_NAME"
	ConsulLockKey    = "CONSUL_LOCK_KEY"
	RedisAddr        = "REDIS_ADDR"
)

type ResecConfig struct {
	consulClientConfig *consulapi.Config
	consulServiceName  string
	consulLockKey      string
	redisAddr          string
	redisHost          string
	redisPort          int
	stopCh             chan interface{}
}

// defaultConfig returns the default configuration for the ReSeC
func DefaultConfig() *ResecConfig {
	config := &ResecConfig{
		consulClientConfig: consulapi.DefaultConfig(),
		consulServiceName: "redis",
		consulLockKey:     "resec/.lock",
		redisAddr:         "127.0.0.1:6379",
		stopCh:            make(chan interface{}),
	}

	if consulServiceName := os.Getenv(ConsulSericeName); consulServiceName != "" {
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
