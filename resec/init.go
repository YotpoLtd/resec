package resec

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-redis/redis"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/logutils"
)

// Environment variables keys
const (
	AnnounceAddr                   = "ANNOUNCE_ADDR"
	ConsulDeregisterServiceAfter   = "CONSUL_DEREGISTER_SERVICE_AFTER"
	ConsulLockKey                  = "CONSUL_LOCK_KEY"
	ConsulLockSessionName          = "CONSUL_LOCK_SESSION_NAME"
	ConsulLockTTL                  = "CONSUL_LOCK_TTL"
	ConsulLockMonitorRetries       = "CONSUL_LOCK_MONITOR_RETRIES"
	ConsulLockMonitorRetryInterval = "CONSUL_LOCK_MONITOR_RETRY_INTERVAL"
	ConsulServiceName              = "CONSUL_SERVICE_NAME"
	ConsulServicePrefix            = "CONSUL_SERVICE_PREFIX"
	HealthCheckInterval            = "HEALTHCHECK_INTERVAL"
	HealthCheckTimeout             = "HEALTHCHECK_TIMEOUT"
	LogLevel                       = "LOG_LEVEL"
	MasterTags                     = "MASTER_TAGS"
	RedisAddr                      = "REDIS_ADDR"
	RedisPassword                  = "REDIS_PASSWORD"
	SlaveTags                      = "SLAVE_TAGS"
)

// consul struct description
type consul struct {
	checkID                  string                 // consul check ID to register our service with
	client                   *consulapi.Client      // consul client used to interact with Consul
	clientConfig             *consulapi.Config      // consul client config
	deregisterServiceAfter   time.Duration          // time to have the consul service in "failed" mode before consul will reap it
	healthy                  bool                   // track if the consul backend is healthy or not
	lock                     *consulapi.Lock        // consul log struct
	lockAbortCh              chan struct{}          // channel will be closed when we want to abort waiting for the consul lock
	lockErrorCh              <-chan struct{}        // channel will be closed by Consul SDK when the held lock fails
	lockIsHeld               bool                   // track if we currently hold the lock (aka redis master)
	lockIsWaiting            bool                   // track if we are waiting for the lock to be released (trying to become master)
	lockKey                  string                 // consul kv path to the lock
	lockMonitorRetries       int                    // consul lock retries if 500 is received
	lockMonitorRetryInterval time.Duration          // consul lock retry time
	lockSessionName          string                 // consul lock session name
	lockStatusCh             chan *consulLockStatus // used to publish consul lock changes internally
	lockStopWaiterHandlerCh  chan bool              // used to gracefully release the held lock
	lockTTL                  time.Duration          // time-to-live for the lock (how frequently we need to update it to keep the ownership)
	lockWaitHandlerRunning   bool                   // track if we are currently waiting to acquire the lock
	serviceID                string                 // consul service ID (consul internal id) to use when registering the service
	serviceName              string                 // the (human) service name to register our service as
	serviceNamePrefix        string                 // an optional prefix for the consul service name
	tags                     map[string][]string    // list of arbitrary consul tags to publish with the service
	ttl                      string
}

// consulLockStatus is our internal representation of the consul lock health / avaibility
type consulLockStatus struct {
	acquired bool  // have we acquired the consul lock or not
	err      error // any errors we might have gotten during the consul lock
}

// redisConnection is our internal representation of redis state
type redisConnection struct {
	address           string        // address to connect to
	password          string        // (optional) redis password
	client            *redis.Client // redis client
	healthy           bool          // wether we can talk to redis or not
	replicationStatus string        // the current replication status (master or slave)
}

// redisInfo struct description
type redisInfo struct {
	address string
	port    int
}

// redisHealth struct description
type redisHealth struct {
	output  string // raw output from redis status
	healthy bool   // wether we successfully could talk to redis or not
}

// app core struct
// holds references to other structs with internal state
type app struct {
	announceAddr          string                         // what IP we should publish to Consul (default "")
	announceHost          string                         // ??
	announcePort          int                            // what Port we should publish to Consul (default "")
	consul                *consul                        // internal Consul state
	healthCheckInterval   time.Duration                  // how frequently we should update our consul check
	healthCheckTimeout    time.Duration                  // how long time the health check is allowed to take
	lastKnownMaster       *consulapi.ServiceEntry        // struct with the info on the last known redis master (from consul catalog)
	lastKnownMasterInfo   redisInfo                      // struct with the info on the last known redis master (internal representation)
	logLevel              string                         // which level we should do logging
	consulMasterServiceCh chan []*consulapi.ServiceEntry // channel where consul updates to the master service will be published
	redis                 *redisConnection               // internal Redis state
	redisReplicationCh    chan *redisHealth              // channel where redis updates will be published
	sigCh                 chan os.Signal                 // signal handler channel
	stopCh                chan interface{}               // stop channel for non-signal termination
}

// setup returns the default configuration for the ReSeC
func Setup() (*app, error) {
	config := &app{
		consul: &consul{
			clientConfig: &consulapi.Config{
				HttpClient: &http.Client{
					Timeout: time.Second * 1,
				},
			},
			deregisterServiceAfter:   time.Hour * 72,
			lockAbortCh:              make(chan struct{}, 1),
			lockKey:                  "resec/.lock",
			lockMonitorRetries:       3,
			lockMonitorRetryInterval: time.Second * 1,
			lockSessionName:          "resec",
			lockStatusCh:             make(chan *consulLockStatus, 1),
			lockTTL:                  time.Second * 15,
			tags:                     make(map[string][]string),
			serviceNamePrefix:        "redis",
		},
		redis: &redisConnection{
			address: "127.0.0.1:6379",
		},
		logLevel:              "INFO",
		healthCheckInterval:   time.Second * 5,
		healthCheckTimeout:    2 * time.Minute,
		consulMasterServiceCh: make(chan []*consulapi.ServiceEntry, 1),
		redisReplicationCh:    make(chan *redisHealth, 1),
		sigCh:                 make(chan os.Signal, 1),
		stopCh:                make(chan interface{}, 1),
	}

	signal.Notify(config.sigCh, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)

	if logLevel := os.Getenv(LogLevel); logLevel != "" {
		config.logLevel = logLevel
	}

	filter := &logutils.LevelFilter{
		Levels:   []logutils.LogLevel{"DEBUG", "INFO", "WARN", "ERROR", "CRITICAL"},
		MinLevel: logutils.LogLevel(config.logLevel),
		Writer:   os.Stderr,
	}
	log.SetOutput(filter)

	// Prefer CONSUL_SERVICE_NAME over CONSUL_SERVICE_PREFIX
	if consulServiceName := os.Getenv(ConsulServiceName); consulServiceName != "" {
		config.consul.serviceName = consulServiceName
	} else if consulServicePrefix := os.Getenv(ConsulServicePrefix); consulServicePrefix != "" {
		config.consul.serviceNamePrefix = consulServicePrefix
	}

	// Fail if CONSUL_SERVICE_NAME is used and no MASTER_TAGS are provided
	if masterTags := os.Getenv(MasterTags); masterTags != "" {
		config.consul.tags["master"] = strings.Split(masterTags, ",")
	} else if config.consul.serviceName != "" {
		return nil, fmt.Errorf("[ERROR] MASTER_TAGS is required when CONSUL_SERVICE_NAME is used")
	}

	if slaveTags := os.Getenv(SlaveTags); slaveTags != "" {
		config.consul.tags["slave"] = strings.Split(slaveTags, ",")
	}

	if config.consul.serviceName != "" {
		if len(config.consul.tags["slave"]) >= 1 && len(config.consul.tags["master"]) >= 1 {
			if config.consul.tags["slave"][0] == config.consul.tags["master"][0] {
				return nil, fmt.Errorf("[PANIC] The first tag in %s and %s must be unique", MasterTags, SlaveTags)
			}
		}
	}

	if consulLockKey := os.Getenv(ConsulLockKey); consulLockKey != "" {
		config.consul.lockKey = consulLockKey
	}

	if consulLockSessionName := os.Getenv(ConsulLockSessionName); consulLockSessionName != "" {
		config.consul.lockSessionName = consulLockSessionName
	}

	if consulLockMonitorRetries := os.Getenv(ConsulLockMonitorRetries); consulLockMonitorRetries != "" {
		consulLockMonitorRetriesInt, err := strconv.Atoi(consulLockMonitorRetries)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as int: %s", ConsulLockMonitorRetries, consulLockMonitorRetries, err)
		}

		config.consul.lockMonitorRetries = consulLockMonitorRetriesInt
	}

	if consulLockMonitorRetryInterval := os.Getenv(ConsulLockMonitorRetryInterval); consulLockMonitorRetryInterval != "" {
		consulLockMonitorRetryIntervalDuration, err := time.ParseDuration(consulLockMonitorRetryInterval)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", ConsulLockMonitorRetryInterval, consulLockMonitorRetryInterval, err)
		}

		config.consul.lockMonitorRetryInterval = consulLockMonitorRetryIntervalDuration
	}

	if healthCheckInterval := os.Getenv(HealthCheckInterval); healthCheckInterval != "" {
		healthCheckIntervalDuration, err := time.ParseDuration(healthCheckInterval)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", HealthCheckInterval, healthCheckInterval, err)
		}

		config.healthCheckInterval = healthCheckIntervalDuration
	}

	// setting Consul Check TTL to be 2 * Check Interval
	config.consul.ttl = time.Duration(config.healthCheckInterval * 2).String()

	if healthCheckTimeout := os.Getenv(HealthCheckTimeout); healthCheckTimeout != "" {
		healthCheckTimeOutDuration, err := time.ParseDuration(healthCheckTimeout)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", HealthCheckTimeout, healthCheckTimeout, err)
		}

		config.healthCheckTimeout = healthCheckTimeOutDuration
	}

	if consulDeregisterServiceAfter := os.Getenv(ConsulDeregisterServiceAfter); consulDeregisterServiceAfter != "" {
		consulDeregisterServiceAfterDuration, err := time.ParseDuration(consulDeregisterServiceAfter)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", ConsulDeregisterServiceAfter, consulDeregisterServiceAfter, err)
		}

		config.consul.deregisterServiceAfter = consulDeregisterServiceAfterDuration

	}

	if consuLockTTL := os.Getenv(ConsulLockTTL); consuLockTTL != "" {
		consuLockTTLDuration, err := time.ParseDuration(consuLockTTL)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", ConsulLockTTL, consuLockTTL, err)
		}

		if consuLockTTLDuration < time.Second*15 {
			return nil, fmt.Errorf("[CRITICAL] Minimum Consul lock session TTL is 15s")
		}

		config.consul.lockTTL = consuLockTTLDuration
	}

	if redisAddr := os.Getenv(RedisAddr); redisAddr != "" {
		config.redis.address = redisAddr
	}

	if redisPassword := os.Getenv(RedisPassword); redisPassword != "" {
		config.redis.password = redisPassword
	}

	// If ANNOUNCE_ADDR is set it will be used for registration in consul
	// otherwise if redis address is provided - it will be used for registration in consul
	// if redis address is localhost only port will be announced to the consul

	if announceAddr := os.Getenv(AnnounceAddr); announceAddr != "" {
		config.announceAddr = announceAddr
	} else {
		redisHost := strings.Split(config.redis.address, ":")[0]
		redisPort := strings.Split(config.redis.address, ":")[1]
		if redisHost == "127.0.0.1" || redisHost == "localhost" || redisHost == "::1" {
			config.announceAddr = ":" + redisPort
		} else {
			config.announceAddr = config.redis.address
		}
	}

	var err error
	config.announceHost = strings.Split(config.announceAddr, ":")[0]
	config.announcePort, err = strconv.Atoi(strings.Split(config.announceAddr, ":")[1])
	if err != nil {
		return nil, fmt.Errorf("[ERROR] Trouble extracting port number from [%s]", config.redis.address)
	}

	// initialise redis as unhealthy
	config.redis.healthy = false

	redisOptions := &redis.Options{
		Addr:        config.redis.address,
		DialTimeout: config.healthCheckTimeout,
		ReadTimeout: config.healthCheckTimeout,
	}

	if config.redis.password != "" {
		redisOptions.Password = config.redis.password
	}

	config.redis.client = redis.NewClient(redisOptions)

	config.consul.client, err = consulapi.NewClient(config.consul.clientConfig)

	if err != nil {
		return nil, fmt.Errorf("[CRITICAL] Can't initialize consul client %s", err)
	}

	return config, nil
}
