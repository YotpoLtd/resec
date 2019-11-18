package consul

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/jpillora/backoff"
	"github.com/seatgeek/resec/resec/redis"
	"github.com/seatgeek/resec/resec/state"
	log "github.com/sirupsen/logrus"
	cli "gopkg.in/urfave/cli.v1"
)

func NewConnection(c *cli.Context, redisConfig redis.Config) (*Manager, error) {
	consulConfig := &config{
		deregisterServiceAfter:   c.Duration("consul-deregister-service-after"),
		lockKey:                  c.String("consul-lock-key"),
		lockMonitorRetries:       c.Int("consul-lock-monitor-retries"),
		lockMonitorRetryInterval: c.Duration("consul-lock-monitor-retry-interval"),
		lockSessionName:          c.String("consul-lock-session-name"),
		lockTTL:                  c.Duration("consul-lock-ttl"),
		serviceName:              c.String("consul-service-name"),
		serviceNamePrefix:        c.String("consul-service-prefix"),
		serviceTTL:               c.Duration("healthcheck-timeout") * 3,
		serviceTagsByRole: map[string][]string{
			"master": make([]string, 0),
			"slave":  make([]string, 0),
		},
	}

	if masterTags := c.String("consul-master-tags"); masterTags != "" {
		consulConfig.serviceTagsByRole["master"] = strings.Split(masterTags, ",")
	} else if consulConfig.serviceName != "" {
		return nil, fmt.Errorf("MASTER_TAGS is required when CONSUL_SERVICE_NAME is used")
	}

	if slaveTags := c.String("consul-slave-tags"); slaveTags != "" {
		consulConfig.serviceTagsByRole["slave"] = strings.Split(slaveTags, ",")
	}

	if consulConfig.serviceName != "" {
		if len(consulConfig.serviceTagsByRole["slave"]) >= 1 && len(consulConfig.serviceTagsByRole["master"]) >= 1 {
			if consulConfig.serviceTagsByRole["slave"][0] == consulConfig.serviceTagsByRole["master"][0] {
				return nil, fmt.Errorf("The first tag in MASTER_TAGS and SLAVE_TAGS must be unique")
			}
		}
	}

	if consulConfig.lockTTL < 15*time.Second {
		return nil, fmt.Errorf("Minimum Consul lock session TTL is 15s")
	}

	announceAddr := c.String("announce-addr")
	if announceAddr == "" {
		redisHost := strings.Split(redisConfig.Address, ":")[0]
		redisPort := strings.Split(redisConfig.Address, ":")[1]
		if redisHost == "127.0.0.1" || redisHost == "localhost" || redisHost == "::1" {
			consulConfig.announceAddr = ":" + redisPort
		} else {
			consulConfig.announceAddr = redisConfig.Address
		}
	}

	var err error
	consulConfig.announceHost = strings.Split(consulConfig.announceAddr, ":")[0]
	consulConfig.announcePort, err = strconv.Atoi(strings.Split(consulConfig.announceAddr, ":")[1])
	if err != nil {
		return nil, fmt.Errorf("Trouble extracting port number from [%s]", redisConfig.Address)
	}

	instance := &Manager{
		backoff: &backoff.Backoff{
			Min:    50 * time.Millisecond,
			Max:    10 * time.Second,
			Factor: 1.5,
			Jitter: false,
		},
		clientConfig: consulapi.DefaultConfig(),
		commandCh:    make(chan Command, 10),
		config:       consulConfig,
		logger:       log.WithField("system", "consul"),
		masterCh:     make(chan interface{}, 1),
		stateCh:      make(chan state.Consul, 10),
		stopCh:       make(chan interface{}, 1),
		state: &state.Consul{
			Healthy: true,
		},
	}

	consulClient, err := consulapi.NewClient(instance.clientConfig)
	if err != nil {
		return nil, err
	}
	instance.client = &liveClient{consulClient}

	return instance, nil
}
