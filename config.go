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
	AnnounceAddr                 = "ANNOUNCE_ADDR"
	ConsulServicePrefix          = "CONSUL_SERVICE_PREFIX"
	ConsulLockKey                = "CONSUL_LOCK_KEY"
	ConsulDeregisterServiceAfter = "CONSUL_DEREGISTER_SERVICE_AFTER"
	ConsulLockTTL                = "CONSUL_LOCK_TTL"
	HealthCheckInterval          = "HEALTHCHECK_INTERVAL"
	HealthCheckTimeout           = "HEATHCHECK_TIMEOUT"
	RedisAddr                    = "REDIS_ADDR"
	RedisPassword                = "REDIS_PASSWORD"
	LogLevel                     = "LOG_LEVEL"
)

type Consul struct {
	ClientConfig            *consulapi.Config
	Client                  *consulapi.Client
	ServiceNamePrefix       string
	DeregisterServiceAfter  time.Duration
	LockAbortCh             chan struct{}
	LockKey                 string
	LockStatus              chan *ConsulLockStatus
	LockErrorCh             <-chan struct{}
	LockIsHeld              bool
	LockIsWaiting           bool
	LockWaitHandlerRunning  bool
	LockStopWaiterHandlerCh chan bool
	Lock                    *consulapi.Lock
	LockTTL                 time.Duration
	TTL                     string
	CheckId                 string
	ServiceId               string
}

type ConsulLockStatus struct {
	Acquired bool
	Error    error
}

type Redis struct {
	Addr              string
	Password          string
	Client            *redis.Client
	Healthy           bool
	ReplicationStatus string
}

type RedisHealth struct {
	Output  string
	Healthy bool
}

type resecConfig struct {
	consul                 *Consul
	redis                  *Redis
	announceAddr           string
	announceHost           string
	announcePort           int
	healthCheckInterval    time.Duration
	healthCheckTimeout     time.Duration
	logLevel               string
	masterConsulServiceCh  chan []*consulapi.ServiceEntry
	redisHealthCh          chan *RedisHealth
	lastKnownMaster        *consulapi.ServiceEntry
	lastKnownMasterAddress string
	lastKnownMasterPort    int
}

// Config returns the default configuration for the ReSeC
func Config() *resecConfig {
	config := &resecConfig{
		consul: &Consul{
			ClientConfig:      consulapi.DefaultConfig(),
			ServiceNamePrefix: "redis",
			LockKey:           "resec/.lock",
			LockStatus:        make(chan *ConsulLockStatus, 1),
			LockAbortCh:       make(chan struct{}, 1),
		},
		redis: &Redis{
			Addr: "127.0.0.1:6379",
		},
		masterConsulServiceCh: make(chan []*consulapi.ServiceEntry, 1),
		redisHealthCh:         make(chan *RedisHealth, 1),
		logLevel:              "INFO",
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
		config.consul.ServiceNamePrefix = consulServiceName
	}

	if consulLockKey := os.Getenv(ConsulLockKey); consulLockKey != "" {
		config.consul.LockKey = consulLockKey
	}

	if healthCheckInterval := os.Getenv(HealthCheckInterval); healthCheckInterval != "" {
		healthCheckIntervalDuration, err := time.ParseDuration(healthCheckInterval)

		if err != nil {
			log.Println("[ERROR] Trouble parsing %s [%s]", HealthCheckInterval, healthCheckInterval)
		}
		config.healthCheckInterval = healthCheckIntervalDuration
	} else {
		config.healthCheckInterval = time.Second * 5
	}

	// setting Consul Check TTL to be 2 * Check Interval
	config.consul.TTL = time.Duration(config.healthCheckInterval * 2).String()

	if healthCheckTimeout := os.Getenv(HealthCheckTimeout); healthCheckTimeout != "" {
		healthCheckTimeOutDuration, err := time.ParseDuration(healthCheckTimeout)
		if err != nil {
			log.Println("[ERROR] Trouble parsing %s [%s]", HealthCheckTimeout, healthCheckTimeout)
		}
		config.healthCheckTimeout = healthCheckTimeOutDuration
	} else {
		config.healthCheckTimeout = time.Second * 2
	}

	if consulDeregisterServiceAfter := os.Getenv(ConsulDeregisterServiceAfter); consulDeregisterServiceAfter != "" {
		consulDeregisterServiceAfterDuration, err := time.ParseDuration(consulDeregisterServiceAfter)
		if err != nil {
			log.Println("[ERROR] Trouble parsing %s [%s]", ConsulDeregisterServiceAfter, consulDeregisterServiceAfter)
		}
		config.healthCheckTimeout = consulDeregisterServiceAfterDuration
	} else {
		config.consul.DeregisterServiceAfter = time.Hour * 72
	}

	if consuLockTTL := os.Getenv(ConsulLockTTL); consuLockTTL != "" {
		consuLockTTLDuration, err := time.ParseDuration(consuLockTTL)
		if err != nil {
			log.Println("[ERROR] Trouble parsing %s [%s]", ConsulLockTTL, consuLockTTL)
		}
		if consuLockTTLDuration < time.Second*15 {
			log.Fatalf("[CRITICAL] Minimum Consul lock session TTL is 15s")
		}
		config.consul.LockTTL = consuLockTTLDuration
	} else {
		config.consul.LockTTL = time.Second * 15
	}

	if redisAddr := os.Getenv(RedisAddr); redisAddr != "" {
		config.redis.Addr = redisAddr
	}

	if redisPassword := os.Getenv(RedisPassword); redisPassword != "" {
		config.redis.Password = redisPassword
	}

	// If CONSUL_ANNOUNCE_ADDRESS is set it will be used for registration in consul
	// otherwise if redis address is provided - it will be used for registration in consul
	// if redis address is localhost only prot will be announced to the consul

	if announceAddr := os.Getenv(AnnounceAddr); announceAddr != "" {
		config.announceAddr = announceAddr
	} else {
		redisHost := strings.Split(config.redis.Addr, ":")[0]
		redisPort := strings.Split(config.redis.Addr, ":")[1]
		if redisHost == "127.0.0.1" || redisHost == "localhost" || redisHost == "::1" {
			config.announceAddr = ":" + redisPort
		} else {
			config.announceAddr = config.redis.Addr
		}
	}

	var err error
	config.announceHost = strings.Split(config.announceAddr, ":")[0]
	config.announcePort, err = strconv.Atoi(strings.Split(config.announceAddr, ":")[1])
	if err != nil {
		log.Printf("[ERROR] Trouble extracting port number from [%s]", config.redis.Addr)
	}

	// initialise redis as unhealthy
	config.redis.Healthy = false

	return config
}
