package main

import (
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/logutils"
)

const (
	AnnounceAddr        = "ANNOUNCE_ADDR"
	ConsulServicePrefix = "CONSUL_SERVICE_PREFIX"
	ConsulLockKey       = "CONSUL_LOCK_KEY"
	HealthCheckInterval = "HEALTHCHECK_INTERVAL"
	HealthCheckTimeout  = "HEATHCHECK_TIMEOUT"
	RedisAddr           = "REDIS_ADDR"
	RedisPassword       = "REDIS_PASSWORD"
	LogLevel            = "LOG_LEVEL"
)

type redisHealth struct {
	Output  string
	Healthy bool
}

type resecConfig struct {
	announceAddr            string
	announceHost            string
	announcePort            int
	consulClientConfig      *consulapi.Config
	consulClient            *consulapi.Client
	consulServiceNamePrefix string
	consulLockKey           string
	consulTTL               string
	consulCheckId           string
	consulServiceId         string
	consulLockIsHeld        bool
	healthCheckInterval     time.Duration
	healthCheckTimeout      time.Duration
	logLevel                string
	redisAddr               string
	redisPassword           string
	redisClient             *redis.Client
	waitingForLock          bool
	redisMonitorEnabled     bool
	masterConsulServiceCh   chan *consulapi.ServiceEntry
	stopWatchCh             chan struct{}
	errCh                   chan error
	lock                    *consulapi.Lock
	LockErrorCh             <-chan struct{}
	lockAbortCh             chan struct{}
	healthCh                chan string
	redisHealthCh           chan *redisHealth
	promoteCh               chan bool
	lastRedisHealthCheckOK  bool
	masterWatchRunning      bool
}

// defaultConfig returns the default configuration for the ReSeC
func defaultConfig() *resecConfig {
	config := &resecConfig{
		consulClientConfig:      consulapi.DefaultConfig(),
		consulServiceNamePrefix: "redis",
		consulLockKey:           "resec/.lock",
		redisAddr:               "127.0.0.1:6379",
		masterConsulServiceCh:   make(chan *consulapi.ServiceEntry, 1),
		stopWatchCh:             make(chan struct{}, 0),
		lockAbortCh:             make(chan struct{}),
		healthCh:                make(chan string),
		redisHealthCh:           make(chan *redisHealth, 1),
		promoteCh:               make(chan bool, 1),
		logLevel:                "DEBUG",
		waitingForLock:          false,
		consulLockIsHeld:        false,
		redisMonitorEnabled:     true,
		masterWatchRunning:      false,
	}

	if logLevel := os.Getenv(LogLevel); logLevel != "" {
		config.logLevel = logLevel
	}

	filter := &logutils.LevelFilter{
		Levels:   []logutils.LogLevel{"DEBUG", "INFO", "WARN", "ERROR", "CRITICAL"},
		MinLevel: logutils.LogLevel(config.logLevel),
		Writer:   os.Stderr,
	}
	log.SetOutput(filter)

	if consulServiceName := os.Getenv(ConsulServicePrefix); consulServiceName != "" {
		config.consulServiceNamePrefix = consulServiceName
	}

	if consulLockKey := os.Getenv(ConsulLockKey); consulLockKey != "" {
		config.consulLockKey = consulLockKey
	}

	if healthCheckInterval := os.Getenv(HealthCheckInterval); healthCheckInterval != "" {
		healthCheckIntervalDuration, err := time.ParseDuration(healthCheckInterval)

		if err != nil {
			log.Println("[ERROR] Trouble parsing %s [%s]", HealthCheckInterval, healthCheckInterval)
		}
		config.healthCheckInterval = healthCheckIntervalDuration
	} else {
		config.healthCheckInterval, _ = time.ParseDuration("5s")
	}

	// setting Consul Check TTL to be 2 * Check Interval
	config.consulTTL = time.Duration(config.healthCheckInterval * 2).String()

	if healthCheckTimeout := os.Getenv(HealthCheckTimeout); healthCheckTimeout != "" {
		healthCheckTimeOutDuration, err := time.ParseDuration(healthCheckTimeout)
		if err != nil {
			log.Println("[ERROR] Trouble parsing %s [%s]", HealthCheckTimeout, healthCheckTimeout)
		}
		config.healthCheckTimeout = healthCheckTimeOutDuration
	} else {
		config.healthCheckTimeout, _ = time.ParseDuration("2s")
	}

	if redisAddr := os.Getenv(RedisAddr); redisAddr != "" {
		config.redisAddr = redisAddr
	}

	if redisPassword := os.Getenv(RedisPassword); redisPassword != "" {
		config.redisPassword = redisPassword
	}

	// If CONSUL_ANNOUNCE_ADDRESS is set it will be used for registration in consul
	// otherwise if redis address is provided - it will be used for registration in consul
	// if redis address is localhost only prot will be announced to the consul

	if announceAddr := os.Getenv(AnnounceAddr); announceAddr != "" {
		config.announceAddr = announceAddr
	} else {
		redisHost := strings.Split(config.redisAddr, ":")[0]
		redisPort := strings.Split(config.redisAddr, ":")[1]
		if redisHost == "127.0.0.1" || redisHost == "localhost" || redisHost == "::1" {
			config.announceAddr = ":" + redisPort
		} else {
			config.announceAddr = config.redisAddr
		}
	}

	announceHost := strings.Split(config.announceAddr, ":")[0]
	announcePort, err := strconv.Atoi(strings.Split(config.announceAddr, ":")[1])
	if err != nil {
		log.Printf("[ERROR] Trouble extracting port number from [%s]", config.redisAddr)
	}
	config.announceHost = announceHost
	config.announcePort = announcePort

	// initialise redis as unhealthy
	config.redisHealthCh <- &redisHealth{
		Output:  "",
		Healthy: false,
	}

	// Initialise promote as false
	config.promoteCh <- false

	return config
}
