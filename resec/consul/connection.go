package consul

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/YotpoLtd/resec/resec/redis"
	"github.com/YotpoLtd/resec/resec/state"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/jpillora/backoff"
	log "github.com/sirupsen/logrus"
	"gopkg.in/urfave/cli.v1"
)

// emit will emit a consul state change to the reconciler
func (c *Connection) emit() {
	c.stateCh <- *c.state
}

// cleanup will do cleanup tasks when the reconciler is shutting down
func (c *Connection) cleanup() {
	c.logger.Debug("Releasing lock")
	c.releaseConsulLock()

	c.logger.Debug("Deregister service")
	c.deregisterService()

	c.logger.Debug("Closing stopCh")
	close(c.stopCh)

	c.state.Stopped = true
	c.emit()
}

// continuouslyAcquireConsulLeadership waits to acquire the lock to the Consul KV key.
// it will run until the stopCh is closed
func (c *Connection) continuouslyAcquireConsulLeadership() {
	t := time.NewTimer(250 * time.Millisecond)

	for {
		select {
		// if closed, we should stop working
		case <-c.stopCh:
			return

		// if consul master service have changes, immidately try to  claim the lock
		// since there is a good chance the service changed because the current master
		// went away
		case <-c.masterCh:
			c.acquireConsulLeadership()

		// Periodically try to acquire the consul lock
		case <-t.C:
			c.acquireConsulLeadership()

			// if we are not healthy, apply exponential backoff
			if c.state.Healthy == false {
				d := c.backoff.Duration()
				c.logger.Warnf("Consul is not healthy, going to apply backoff of %s until next attempt", d.Round(time.Second).String())
				t.Reset(d)
				continue
			}

			t.Reset(250 * time.Millisecond)
		}
	}
}

// acquireConsulLeadership will one-off try to acquire the consul lock needed to become
// redis Master node
func (c *Connection) acquireConsulLeadership() {
	// if we already hold the lock, we can't acquire it again
	if c.state.Master {
		c.logger.Debug("We already hold the lock, can't acquire it again")
		return
	}

	// create the lock structs
	lockOptions := &consulapi.LockOptions{
		Key:              c.config.lockKey,
		SessionName:      c.config.lockSessionName,
		SessionTTL:       c.config.lockTTL.String(),
		MonitorRetries:   c.config.lockMonitorRetries,
		MonitorRetryTime: c.config.lockMonitorRetryInterval,
	}

	var err error
	c.config.lock, err = c.client.LockOpts(lockOptions)
	if err != nil {
		c.logger.Error("Failed create lock options: %+v", err)
		return
	}

	// try to acquire the lock
	c.logger.Infof("Trying to acquire consul lock")
	c.lockErrorCh, err = c.config.lock.Lock(c.lockCh)
	c.handleConsulError(err)
	if err != nil {
		return
	}

	c.logger.Info("Lock successfully acquired")

	c.state.Master = true
	c.emit()

	//
	// start monitoring the consul lock for errors / changes
	//

	c.config.voluntarilyReleaseLockCh = make(chan interface{})

	// At this point, if we return from this function, we need to make sure
	// we release the lock
	defer func() {
		err := c.config.lock.Unlock()
		c.handleConsulError(err)
		if err != nil {
			c.logger.Errorf("Could not release Consul Lock: %v", err)
		} else {
			c.logger.Info("Consul Lock successfully released")
		}

		c.state.Master = false
		c.emit()
	}()

	// Wait for changes to Consul Lock
	for {
		select {
		// Global stop of all go-routines, reconciler is shutting down
		case <-c.stopCh:
			return

		// Changes on the lock error channel
		// if the channel is closed, it mean that we no longer hold the lock
		// if written to, we simply pass on the message
		case data, ok := <-c.lockErrorCh:
			if !ok {
				c.logger.Error("Consul Lock error channel was closed, we no longer hold the lock")
				return
			}

			c.logger.Warnf("Something wrote to lock error channel %+v", data)

		// voluntarily release our claim on the lock
		case <-c.config.voluntarilyReleaseLockCh:
			c.logger.Warnf("Voluntarily releasing the Consul lock")
			return
		}
	}
}

// releaseConsulLock stops consul lock handler")
func (c *Connection) releaseConsulLock() {
	if c.state.Master == false {
		c.logger.Debug("Can't release Consul lock, we don't have it")
		return
	}

	c.logger.Info("Releasing Consul lock")
	close(c.config.voluntarilyReleaseLockCh)
}

// getReplicationStatus will return current replication status
// the default value is 'slave' - only if we hold the lock will 'master' be returned
func (c *Connection) getReplicationStatus() string {
	if c.state.Master {
		return "master"
	}

	return "slave"
}

// getConsulServiceName will return the consul service name to use
// depending on using tagged service (replication status as tag) or
// service name (with replication status as suffix)
func (c *Connection) getConsulServiceName() string {
	name := c.config.serviceName

	if name == "" {
		name = c.config.serviceNamePrefix + "-" + c.getReplicationStatus()
	}

	return name
}

// getConsulServiceID will return the consul service ID
// it will either use the service name, or the service name prefix depending
// on your configuration
func (c *Connection) getConsulServiceID() string {
	ID := c.config.serviceName

	if c.config.serviceName == "" {
		ID = c.config.serviceNamePrefix
	}

	return ID
}

// registerService registers a service in consul
func (c *Connection) registerService(redisState state.Redis) {
	serviceID := c.getConsulServiceID()
	replicationStatus := c.getReplicationStatus()

	c.config.serviceID = serviceID + "@" + c.config.announceAddr
	c.config.checkID = c.config.serviceID + ":replication-status-check"

	serviceInfo := &consulapi.AgentServiceRegistration{
		ID:   c.config.serviceID,
		Port: c.config.announcePort,
		Name: c.getConsulServiceName(),
		Tags: c.config.serviceTagsByRole[replicationStatus],
	}

	if c.config.announceHost != "" {
		serviceInfo.Address = c.config.announceHost
	}

	c.logger.Infof("Registering %s service in consul", serviceInfo.Name)

	err := c.client.Agent().ServiceRegister(serviceInfo)
	c.handleConsulError(err)
	if err != nil {
		return
	}

	c.logger.Infof("Registered service %s (%s) with address %s:%d", serviceInfo.Name, serviceInfo.ID, serviceInfo.Address, serviceInfo.Port)

	check := &consulapi.AgentCheckRegistration{
		Name:      "Resec: " + replicationStatus + " replication status",
		ID:        c.config.checkID,
		ServiceID: c.config.serviceID,
		AgentServiceCheck: consulapi.AgentServiceCheck{
			TTL:    c.config.serviceTTL.String(),
			Status: "warning",
			DeregisterCriticalServiceAfter: c.config.deregisterServiceAfter.String(),
		},
	}

	err = c.client.Agent().CheckRegister(check)
	c.handleConsulError(err)
}

// deregisterService will deregister the consul service from Consul catalog
func (c *Connection) deregisterService() {
	if c.state.Healthy && c.config.serviceID != "" {
		err := c.client.Agent().ServiceDeregister(c.config.serviceID)
		c.handleConsulError(err)
		if err != nil {
			c.logger.Errorf("Can't deregister consul service: %s", err)
		}
	}

	c.config.serviceID = ""
	c.config.checkID = ""
}

// setConsulCheckStatus sets consul status check TTL and output
func (c *Connection) setConsulCheckStatus(redisState state.Redis) {
	if c.config.checkID == "" {
		c.registerService(redisState)
	}

	c.logger.Debug("Updating Check TTL for service")
	err := c.client.Agent().UpdateTTL(c.config.checkID, redisState.ReplicationString, "passing")
	c.handleConsulError(err)
}

// getConsulMasterDetails will return the Consul service name and tag
// that matches what the Resec acting as master will expose in Consul
func (c *Connection) getConsulMasterDetails() (serviceName string, serviceTag string) {
	serviceName = c.config.serviceNamePrefix + "-master"

	if c.config.serviceName != "" {
		serviceName = c.config.serviceName
		serviceTag = c.config.serviceTagsByRole["master"][0]
	}

	return serviceName, serviceTag
}

// watchConsulMasterService starts watching the service (+ tags) that represents the
// redis master service. All changes will be emitted to masterConsulServiceCh.
func (c *Connection) watchConsulMasterService() error {
	serviceName, serviceTag := c.getConsulMasterDetails()

	q := &consulapi.QueryOptions{
		WaitIndex: 0,
		WaitTime:  time.Second,
	}

	d := 250 * time.Millisecond
	t := time.NewTimer(d)

	for {
		select {

		case <-c.stopCh:
			return nil

		case <-t.C:
			services, meta, err := c.client.Health().Service(serviceName, serviceTag, true, q)
			c.handleConsulError(err)
			if err != nil {
				t.Reset(c.backoff.ForAttempt(c.backoff.Attempt()))
				continue
			}

			t.Reset(d)

			if q.WaitIndex == meta.LastIndex {
				c.logger.Debugf("No change in master service health")
				continue
			}

			q.WaitIndex = meta.LastIndex

			if len(services) == 0 {
				c.logger.Warn("No (healthy) master service found in Consul catalog")
				continue
			}

			if len(services) > 1 {
				c.logger.Error("More than 1 (healthy) master service found in Consul catalog")
				continue
			}

			master := services[0]

			if c.state.MasterAddr == master.Node.Address && c.state.MasterPort == master.Service.Port {
				c.logger.Debugf("No change in master service configuration")
				continue
			}

			c.state.MasterAddr = master.Node.Address
			c.state.MasterPort = master.Service.Port
			c.emit()

			c.logger.Infof("Saw change in master service. New IP+Port is: %s:%d", c.state.MasterAddr, c.state.MasterPort)
			c.masterCh <- true
		}
	}
}

// handleConsulError is the error handler
func (c *Connection) handleConsulError(err error) {
	// if no error
	if err == nil {
		// if state is healthy do nothing
		if c.state.Healthy {
			return
		}

		// reset exponential backoff
		c.backoff.Reset()

		// mark us as healthy
		c.state.Healthy = true
		c.emit()

		return
	}

	// if we get connection refused, we are not healthy
	if c.state.Healthy && strings.Contains(err.Error(), "dial tcp") {
		c.state.Healthy = false
		c.emit()

		c.deregisterService()

	}

	// if the check don't have a TTL, it mean that the service + check is gone
	// deregister the service+check so we can register a new one
	if strings.Contains(err.Error(), "does not have associated TTL") {
		c.deregisterService()
	}

	c.logger.Errorf("Consul error: %v", err)
}

func (c *Connection) CommandRunner() {
	for {
		select {

		case <-c.stopCh:
			return

		case payload := <-c.commandCh:
			switch payload.name {
			case RegisterServiceCommand:
				c.registerService(payload.redisState)
			case DeregisterServiceCommand:
				c.deregisterService()
			case UpdateServiceCommand:
				c.setConsulCheckStatus(payload.redisState)
			case ReleaseLockCommand:
				c.releaseConsulLock()
			case StartCommand:
				c.start()
			case StopConsulCommand:
				c.cleanup()
			}
		}
	}
}

func (c *Connection) start() {
	go c.continuouslyAcquireConsulLeadership()
	go c.watchConsulMasterService()

	c.state.Ready = true
	c.emit()
}

func (c *Connection) StateChReader() <-chan state.Consul {
	return c.stateCh
}

func (c *Connection) CommandChWriter() chan<- Command {
	return c.commandCh
}

func NewConnection(c *cli.Context, redisConfig redis.RedisConfig) (*Connection, error) {
	consulConfig := &config{
		deregisterServiceAfter:   c.Duration("consul-deregister-service-after"),
		lockKey:                  c.String("consul-lock-key"),
		lockMonitorRetries:       c.Int("consul-lock-monitor-retries"),
		lockMonitorRetryInterval: c.Duration("consul-lock-monitor-retry-interval"),
		lockSessionName:          c.String("consul-lock-session-name"),
		lockTTL:                  c.Duration("consul-lock-ttl"),
		serviceName:              c.String("consul-service-name"),
		serviceNamePrefix:        c.String("consul-service-prefix"),
		serviceTTL:               c.Duration("healthcheck-timeout") * 2,
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

	instance := &Connection{
		backoff: &backoff.Backoff{
			Min:    50 * time.Millisecond,
			Max:    10 * time.Second,
			Factor: 1.5,
			Jitter: false,
		},
		clientConfig: consulapi.DefaultConfig(),
		commandCh:    make(chan Command, 1),
		config:       consulConfig,
		logger:       log.WithField("system", "consul"),
		masterCh:     make(chan interface{}, 1),
		stateCh:      make(chan state.Consul),
		stopCh:       make(chan interface{}, 1),
		state: &state.Consul{
			Healthy: true,
		},
	}

	instance.client, err = consulapi.NewClient(instance.clientConfig)
	if err != nil {
		return nil, err
	}

	return instance, nil
}
