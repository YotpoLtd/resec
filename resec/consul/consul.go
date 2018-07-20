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
func (cc *Connection) emit() {
	cc.StateCh <- *cc.state
}

// cleanup will do cleanup tasks when the reconciler is shutting down
func (cc *Connection) cleanup() {
	cc.logger.Debug("Releasing lock")
	cc.releaseConsulLock()

	cc.logger.Debug("Deregister service")
	cc.deregisterService()

	cc.logger.Debug("Closing stopCh")
	close(cc.stopCh)
}

// continuouslyAcquireConsulLeadership waits to acquire the lock to the Consul KV key.
// it will run until the stopCh is closed
func (cc *Connection) continuouslyAcquireConsulLeadership() {
	t := time.NewTimer(250 * time.Millisecond)

	for {
		select {
		// if closed, we should stop working
		case <-cc.stopCh:
			return

		// if consul master service have changes, immidately try to  claim the lock
		// since there is a good chance the service changed because the current master
		// went away
		case <-cc.masterCh:
			cc.acquireConsulLeadership()

		// Periodically try to acquire the consul lock
		case <-t.C:
			cc.acquireConsulLeadership()

			// if we are not healthy, apply exponential backoff
			if cc.state.Healthy == false {
				d := cc.backoff.Duration()
				cc.logger.Warnf("Consul is not healthy, going to apply backoff of %s until next attempt", d.Round(time.Second).String())
				t.Reset(d)
				continue
			}

			t.Reset(250 * time.Millisecond)
		}
	}
}

// acquireConsulLeadership will one-off try to acquire the consul lock needed to become
// redis Master node
func (cc *Connection) acquireConsulLeadership() {
	// if we already hold the lock, we can't acquire it again
	if cc.state.LockIsHeld {
		cc.logger.Debug("We already hold the lock, can't acquire it again")
		return
	}

	// create the lock structs
	lockOptions := &consulapi.LockOptions{
		Key:              cc.config.lockKey,
		SessionName:      cc.config.lockSessionName,
		SessionTTL:       cc.config.lockTTL.String(),
		MonitorRetries:   cc.config.lockMonitorRetries,
		MonitorRetryTime: cc.config.lockMonitorRetryInterval,
	}

	var err error
	cc.config.lock, err = cc.client.LockOpts(lockOptions)
	if err != nil {
		cc.logger.Error("Failed create lock options: %+v", err)
		return
	}

	// try to acquire the lock
	cc.logger.Infof("Trying to acquire consul lock")
	cc.lockErrorCh, err = cc.config.lock.Lock(cc.lockCh)
	cc.handleConsulError(err)
	if err != nil {
		return
	}

	cc.logger.Info("Lock successfully acquired")

	cc.state.LockIsHeld = true
	cc.emit()

	//
	// start monitoring the consul lock for errors / changes
	//

	cc.config.voluntarilyReleaseLockCh = make(chan interface{})

	// At this point, if we return from this function, we need to make sure
	// we release the lock
	defer func() {
		err := cc.config.lock.Unlock()
		cc.handleConsulError(err)
		if err != nil {
			cc.logger.Errorf("Could not release Consul Lock: %v", err)
		} else {
			cc.logger.Info("Consul Lock successfully released")
		}

		cc.state.LockIsHeld = false
		cc.emit()
	}()

	// Wait for changes to Consul Lock
	for {
		select {
		// Global stop of all go-routines, reconciler is shutting down
		case <-cc.stopCh:
			return

		// Changes on the lock error channel
		// if the channel is closed, it mean that we no longer hold the lock
		// if written to, we simply pass on the message
		case data, ok := <-cc.lockErrorCh:
			if !ok {
				cc.logger.Error("Consul Lock error channel was closed, we no longer hold the lock")
				return
			}

			cc.logger.Warnf("Something wrote to lock error channel %+v", data)

		// voluntarily release our claim on the lock
		case <-cc.config.voluntarilyReleaseLockCh:
			cc.logger.Warnf("Voluntarily releasing the Consul lock")
			return
		}
	}
}

// releaseConsulLock stops consul lock handler")
func (cc *Connection) releaseConsulLock() {
	if cc.state.LockIsHeld == false {
		cc.logger.Debug("Can't release Consul lock, we don't have it")
		return
	}

	cc.logger.Info("Releasing Consul lock")
	close(cc.config.voluntarilyReleaseLockCh)
}

// getReplicationStatus will return current replication status
// the default value is 'slave' - only if we hold the lock will 'master' be returned
func (cc *Connection) getReplicationStatus() string {
	if cc.state.LockIsHeld {
		return "master"
	}

	return "slave"
}

// getConsulServiceName will return the consul service name to use
// depending on using tagged service (replication status as tag) or
// service name (with replication status as suffix)
func (cc *Connection) getConsulServiceName() string {
	name := cc.config.serviceName

	if name == "" {
		name = cc.config.serviceNamePrefix + "-" + cc.getReplicationStatus()
	}

	return name
}

// getConsulServiceID will return the consul service ID
// it will either use the service name, or the service name prefix depending
// on your configuration
func (cc *Connection) getConsulServiceID() string {
	ID := cc.config.serviceName

	if cc.config.serviceName == "" {
		ID = cc.config.serviceNamePrefix
	}

	return ID
}

// registerService registers a service in consul
func (cc *Connection) registerService(redisState state.Redis) {
	serviceID := cc.getConsulServiceID()
	replicationStatus := cc.getReplicationStatus()

	cc.config.serviceID = serviceID + "@" + cc.config.announceAddr
	cc.config.checkID = cc.config.serviceID + ":replication-status-check"

	serviceInfo := &consulapi.AgentServiceRegistration{
		ID:   cc.config.serviceID,
		Port: cc.config.announcePort,
		Name: cc.getConsulServiceName(),
		Tags: cc.config.serviceTagsByRole[replicationStatus],
	}

	if cc.config.announceHost != "" {
		serviceInfo.Address = cc.config.announceHost
	}

	cc.logger.Infof("Registering %s service in consul", serviceInfo.Name)

	err := cc.client.Agent().ServiceRegister(serviceInfo)
	cc.handleConsulError(err)
	if err != nil {
		return
	}

	cc.logger.Infof("Registered service %s (%s) with address %s:%d", serviceInfo.Name, serviceInfo.ID, serviceInfo.Address, serviceInfo.Port)

	check := &consulapi.AgentCheckRegistration{
		Name:      "Resec: " + replicationStatus + " replication status",
		ID:        cc.config.checkID,
		ServiceID: cc.config.serviceID,
		AgentServiceCheck: consulapi.AgentServiceCheck{
			TTL:    cc.config.serviceTTL.String(),
			Status: "warning",
			DeregisterCriticalServiceAfter: cc.config.deregisterServiceAfter.String(),
		},
	}

	err = cc.client.Agent().CheckRegister(check)
	cc.handleConsulError(err)
}

// deregisterService will deregister the consul service from Consul catalog
func (cc *Connection) deregisterService() {
	if cc.state.Healthy && cc.config.serviceID != "" {
		err := cc.client.Agent().ServiceDeregister(cc.config.serviceID)
		cc.handleConsulError(err)
		if err != nil {
			cc.logger.Errorf("Can't deregister consul service: %s", err)
		}
	}

	cc.config.serviceID = ""
	cc.config.checkID = ""
}

// setConsulCheckStatus sets consul status check TTL and output
func (cc *Connection) setConsulCheckStatus(redisState state.Redis) {
	if cc.config.checkID == "" {
		cc.registerService(redisState)
	}

	cc.logger.Debug("Updating Check TTL for service")
	err := cc.client.Agent().UpdateTTL(cc.config.checkID, redisState.ReplicationString, "passing")
	cc.handleConsulError(err)
}

// getConsulMasterDetails will return the Consul service name and tag
// that matches what the Resec acting as master will expose in Consul
func (cc *Connection) getConsulMasterDetails() (serviceName string, serviceTag string) {
	serviceName = cc.config.serviceNamePrefix + "-master"

	if cc.config.serviceName != "" {
		serviceName = cc.config.serviceName
		serviceTag = cc.config.serviceTagsByRole["master"][0]
	}

	return serviceName, serviceTag
}

// watchConsulMasterService starts watching the service (+ tags) that represents the
// redis master service. All changes will be emitted to masterConsulServiceCh.
func (cc *Connection) watchConsulMasterService() error {
	serviceName, serviceTag := cc.getConsulMasterDetails()

	q := &consulapi.QueryOptions{
		WaitIndex: 0,
		WaitTime:  time.Second,
	}

	d := 250 * time.Millisecond
	t := time.NewTimer(d)

	for {
		select {

		case <-cc.stopCh:
			return nil

		case <-t.C:
			services, meta, err := cc.client.Health().Service(serviceName, serviceTag, true, q)
			cc.handleConsulError(err)
			if err != nil {
				t.Reset(cc.backoff.ForAttempt(cc.backoff.Attempt()))
				continue
			}

			t.Reset(d)

			if q.WaitIndex == meta.LastIndex {
				cc.logger.Debugf("No change in master service health")
				continue
			}

			q.WaitIndex = meta.LastIndex

			if len(services) == 0 {
				cc.logger.Warn("No (healthy) master service found in Consul catalog")
				continue
			}

			if len(services) > 1 {
				cc.logger.Error("More than 1 (healthy) master service found in Consul catalog")
				continue
			}

			master := services[0]

			if cc.state.MasterAddr == master.Node.Address && cc.state.MasterPort == master.Service.Port {
				cc.logger.Debugf("No change in master service configuration")
				continue
			}

			cc.state.MasterAddr = master.Node.Address
			cc.state.MasterPort = master.Service.Port
			cc.emit()

			cc.logger.Infof("Saw change in master service. New IP+Port is: %s:%d", cc.state.MasterAddr, cc.state.MasterPort)
			cc.masterCh <- true
		}
	}
}

// handleConsulError is the error handler
func (cc *Connection) handleConsulError(err error) {
	// if no error
	if err == nil {
		// if state is healthy do nothing
		if cc.state.Healthy {
			return
		}

		// reset exponential backoff
		cc.backoff.Reset()

		// mark us as healthy
		cc.state.Healthy = true
		cc.emit()

		return
	}

	// if we get connection refused, we are not healthy
	if cc.state.Healthy && strings.Contains(err.Error(), "dial tcp") {
		cc.state.Healthy = false
		cc.emit()

		cc.deregisterService()

	}

	// if the check don't have a TTL, it mean that the service + check is gone
	// deregister the service+check so we can register a new one
	if strings.Contains(err.Error(), "does not have associated TTL") {
		cc.deregisterService()
	}

	cc.logger.Errorf("Consul error: %v", err)
}

func (cc *Connection) CommandRunner() {
	for {
		select {

		case <-cc.stopCh:
			return

		case payload := <-cc.CommandCh:
			switch payload.kind {
			case RegisterServiceCommand:
				cc.registerService(payload.redisState)
			case DeregisterServiceCommand:
				cc.deregisterService()
			case UpdateServiceCommand:
				cc.setConsulCheckStatus(payload.redisState)
			case ReleaseLockCommand:
				cc.releaseConsulLock()
			case StartCommand:
				cc.start()
			case StopConsulCommand:
				cc.cleanup()
			}
		}
	}
}

func (cc *Connection) start() {
	go cc.continuouslyAcquireConsulLeadership()
	go cc.watchConsulMasterService()

	cc.state.Ready = true
	cc.emit()
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
		CommandCh:    make(chan Command, 1),
		config:       consulConfig,
		logger:       log.WithField("system", "consul"),
		masterCh:     make(chan interface{}, 1),
		StateCh:      make(chan state.Consul),
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
