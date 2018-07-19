package resec

import (
	"fmt"
	"os"
	"time"
)

type config struct {
	healthCheckTimeout  time.Duration
	healthCheckInterval time.Duration
}

func newConfig() (*config, error) {
	config := &config{
		healthCheckTimeout:  2 * time.Minute,
		healthCheckInterval: 5 * time.Second,
	}

	if healthCheckTimeout := os.Getenv(HealthCheckTimeout); healthCheckTimeout != "" {
		healthCheckTimeOutDuration, err := time.ParseDuration(healthCheckTimeout)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", HealthCheckTimeout, healthCheckTimeout, err)
		}

		config.healthCheckTimeout = healthCheckTimeOutDuration
	}

	if healthCheckInterval := os.Getenv(HealthCheckInterval); healthCheckInterval != "" {
		healthCheckIntervalDuration, err := time.ParseDuration(healthCheckInterval)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", HealthCheckInterval, healthCheckInterval, err)
		}

		config.healthCheckInterval = healthCheckIntervalDuration
	}

	return config, nil
}
