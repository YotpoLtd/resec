package resec

import (
	"os"
	"os/signal"
	"syscall"

	log "github.com/sirupsen/logrus"
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

// setup returns the default configuration for the ReSeC
func Setup() (*reconsiler, error) {
	log.SetLevel(log.DebugLevel)

	config, err := newConfig()
	if err != nil {
		log.Fatal(err)
	}

	redisConnection, err := newRedisConnection(config)
	if err != nil {
		log.Fatal(err)
	}

	consulConnection, err := newConsulConnection(config, redisConnection.config)
	if err != nil {
		log.Fatal(err)
	}

	reconsiler := &reconsiler{}
	reconsiler.consulConnection = consulConnection
	reconsiler.redisConnection = redisConnection

	reconsiler.sigCh = make(chan os.Signal, 1)
	reconsiler.stopCh = make(chan interface{}, 1)
	signal.Notify(reconsiler.sigCh, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)

	return reconsiler, nil
}
