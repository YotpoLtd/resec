package resec

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/jpillora/backoff"
	log "github.com/sirupsen/logrus"
)

type consulConnection struct {
	backoff      *backoff.Backoff
	logger       *log.Entry        // logger for the consul connection struct
	client       *consulapi.Client // consul API client
	clientConfig *consulapi.Config // consul API client configuration
	config       *consulConfig     // configuration for this connection
	state        *consulState      // state used by the reconsiler
	stateCh      chan consulState  // state channel used to notify the reconsiler of changes
	lockCh       <-chan struct{}   // lock channel used by Consul SDK to notify about changes
	lockErrorCh  <-chan struct{}   // lock error channel used by Consul SDK to notify about errors related to the lock
	stopCh       chan interface{}  // internal channel used to stop all go-routines when gracefully shutting down
	masterCh     chan interface{}  // notification channel used to notify the Consul Lock go-routing that the master service changed
}

// Consul state used by the reconsiler to decide what actions to take
type consulState struct {
	ready      bool
	healthy    bool
	err        error
	lockIsHeld bool
	masterAddr string
	masterPort int
}

// Consul config used for internal state management
type consulConfig struct {
	announceAddr             string
	announceHost             string
	announcePort             int
	checkID                  string
	deregisterServiceAfter   time.Duration
	lock                     *consulapi.Lock
	lockKey                  string
	lockMonitorRetries       int
	lockMonitorRetryInterval time.Duration
	lockSessionName          string
	voluntarilyReleaseLockCh chan interface{}
	lockTTL                  time.Duration
	monitorRetries           int
	monitorRetryTime         time.Duration
	serviceID                string
	serviceName              string
	serviceNamePrefix        string
	sessionName              string
	sessionTTL               string
	tags                     map[string][]string
	ttl                      time.Duration
}

// emit will emit a consul state change to the reconsiler
func (cc *consulConnection) emit(err error) {
	cc.state.err = err
	cc.stateCh <- *cc.state
}

// cleanup will do cleanup tasks when the reconsiler is shutting down
func (cc *consulConnection) cleanup() {
	cc.logger.Debug("Releasing lock")
	cc.releaseConsulLock()

	cc.logger.Debug("Deregister service")
	cc.deregisterService()

	cc.logger.Debug("Closing stopCh")
	close(cc.stopCh)
}

// continuouslyAcquireConsulLeadership waits to acquire the lock to the Consul KV key.
// it will run until the stopCh is closed
func (cc *consulConnection) continuouslyAcquireConsulLeadership() {
	t := time.NewTimer(250 * time.Millisecond)

	for {
		select {
		case <-cc.stopCh:
			return

		case <-cc.masterCh:
			cc.acquireConsulLeadership()

		case <-t.C:
			cc.acquireConsulLeadership()

			if cc.state.healthy == false {
				d := cc.backoff.Duration()
				cc.logger.Warnf("Backend is unhealthy, going to wait %s until next attempt", d.String())
				t.Reset(d)
				continue
			}

			t.Reset(250 * time.Millisecond)
		}
	}
}

// acquireConsulLeadership will one-off try to acquire the consul lock needed to become
// redis Master node
func (cc *consulConnection) acquireConsulLeadership() {
	// if we already hold the lock, we can't acquire it again
	if cc.state.lockIsHeld {
		cc.logger.Debug("Lock is already held")
		return
	}

	// create the lock structs
	cc.logger.Debugf("Creating Lock options")
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
	cc.logger.Infof("Trying to acquire consul leadership")
	cc.lockErrorCh, err = cc.config.lock.Lock(cc.lockCh)
	cc.handleConsulError(err)
	if err != nil {
		return
	}

	cc.logger.Info("Lock successfully acquired")

	cc.state.lockIsHeld = true
	cc.emit(nil)

	//
	// start monitoring the consul lock for errors / changes
	//

	cc.config.voluntarilyReleaseLockCh = make(chan interface{})

	defer func() {
		err := cc.config.lock.Unlock()
		if err != nil {
			cc.logger.Errorf("Can't release consul lock: %v", err)
		} else {
			cc.logger.Debug("lock released!")
		}

		cc.deregisterService()

		cc.state.lockIsHeld = false
		cc.emit(nil)
	}()

	for {
		select {
		// global stop of all go-routines, reconsiler is shutting down
		case <-cc.stopCh:
			return

		// we lost the lock if the channel is closed
		case data, ok := <-cc.lockErrorCh:
			if !ok {
				cc.logger.Error("Lock error channel was closed, lock is no longer held")
				return
			}

			cc.logger.Debugf("Something wrote to lock error channel %+v", data)

		// voluntarily release our claim on the lock
		case <-cc.config.voluntarilyReleaseLockCh:
			cc.logger.Warnf("Voluntarily releasing the lock")
			return
		}
	}
}

// releaseConsulLock stops consul lock handler")
func (cc *consulConnection) releaseConsulLock() {
	if cc.state.lockIsHeld == false {
		cc.logger.Debug("Lock is not held, nothing to release")
		return
	}

	cc.logger.Debug("Lock is held, releasing")
	close(cc.config.voluntarilyReleaseLockCh)
}

// registerService registers a service in consul
func (cc *consulConnection) registerService(redisState redisState) error {
	replicationStatus := "slave"
	if cc.state.lockIsHeld {
		replicationStatus = "master"
	}

	nameToRegister := cc.config.serviceName
	serviceID := cc.config.serviceName

	if nameToRegister == "" {
		nameToRegister = cc.config.serviceNamePrefix + "-" + replicationStatus
		serviceID = cc.config.serviceNamePrefix
	}

	cc.config.serviceID = serviceID + "@" + cc.config.announceAddr
	cc.config.checkID = cc.config.serviceID + ":replication-status-check"

	serviceInfo := &consulapi.AgentServiceRegistration{
		ID:   cc.config.serviceID,
		Port: cc.config.announcePort,
		Name: nameToRegister,
		Tags: cc.config.tags[replicationStatus],
	}

	if cc.config.announceHost != "" {
		serviceInfo.Address = cc.config.announceHost
	}

	cc.logger.Debugf("Registering %s service in consul", serviceInfo.Name)

	err := cc.client.Agent().ServiceRegister(serviceInfo)
	cc.handleConsulError(err)

	if err != nil {
		return err
	}

	cc.logger.Infof("Registered service [%s](id [%s]) with address [%s:%d]", serviceInfo.Name, serviceInfo.ID, serviceInfo.Address, serviceInfo.Port)
	cc.logger.Debugf("Adding TTL Check with id %s to service %s with id %s", cc.config.checkID, nameToRegister, serviceInfo.ID)

	checkNameToRegister := cc.config.serviceName

	if checkNameToRegister == "" {
		checkNameToRegister = cc.config.serviceNamePrefix
	}

	check := &consulapi.AgentCheckRegistration{
		Name:      "Resec: " + checkNameToRegister + " " + replicationStatus + " replication status",
		ID:        cc.config.checkID,
		ServiceID: cc.config.serviceID,
		AgentServiceCheck: consulapi.AgentServiceCheck{
			TTL:    (10 * time.Second).String(),
			Status: "warning",
			DeregisterCriticalServiceAfter: cc.config.deregisterServiceAfter.String(),
		},
	}

	err = cc.client.Agent().CheckRegister(check)
	cc.handleConsulError(err)
	if err != nil {
		cc.logger.Errorf("Consul Check registration failed: %s", err)
		return err
	}

	cc.logger.Debugf("TTL Check added with id %s to service %s with id %s", cc.config.checkID, nameToRegister, serviceInfo.ID)
	return nil
}

// deregisterService will deregister the consul service from Consul catalog
func (cc *consulConnection) deregisterService() {
	if cc.state.healthy && cc.config.serviceID != "" {
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
func (cc *consulConnection) setConsulCheckStatus(redisState redisState) {
	if cc.config.checkID == "" {
		cc.registerService(redisState)
	}

	err := cc.client.Agent().UpdateTTL(cc.config.checkID, redisState.replicationString, "passing")
	cc.handleConsulError(err)
}

// watchConsulMasterService starts watching the service (+ tags) that represents the
// redis master service. All changes will be emitted to masterConsulServiceCh.
func (cc *consulConnection) watchConsulMasterService() error {
	serviceName := cc.config.serviceNamePrefix + "-master"
	serviceTag := ""

	if cc.config.serviceName != "" {
		serviceName = cc.config.serviceName
		serviceTag = cc.config.tags["master"][0]
	}

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
				cc.logger.Info("0 master services found in Consul catalog")
				continue
			}

			if len(services) > 1 {
				cc.logger.Warn("More than 1 master service found in Consul catalog")
				continue
			}

			master := services[0]

			cc.state.masterAddr = master.Node.Address
			cc.state.masterPort = master.Service.Port
			cc.emit(nil)

			cc.logger.Infof("Saw change in master service. New IP+Port is: %s:%d", cc.state.masterAddr, cc.state.masterPort)
			cc.masterCh <- true
			cc.logger.Infof("DÅNE")
		}
	}
}

// handleConsulError is the error handler
func (cc *consulConnection) handleConsulError(err error) {
	// if no error
	if err == nil {
		// if state is healthy do nothing
		if cc.state.healthy {
			return
		}

		// reset exponential backoff
		cc.backoff.Reset()

		// mark us as healthy
		cc.state.healthy = true
		cc.emit(err)

		return
	}

	// if we get connection refused, we are not healthy
	if cc.state.healthy && strings.Contains(err.Error(), "dial tcp") {
		cc.state.healthy = false
		cc.emit(err)

		cc.deregisterService()

	}

	// if the check don't have a TTL, it mean that the service + check is gone
	// deregister the service+check so we can register a new one
	if strings.Contains(err.Error(), "does not have associated TTL") {
		cc.deregisterService()
	}

	cc.logger.Errorf("Consul error: %v", err)
}

func (cc *consulConnection) start() {
	go cc.continuouslyAcquireConsulLeadership()
	go cc.watchConsulMasterService()

	cc.state.ready = true
	cc.emit(nil)
}

func newConsulConnection(config *config, redisConfig *redisConfig) (*consulConnection, error) {
	consulConfig := &consulConfig{}
	consulConfig.deregisterServiceAfter = 72 * time.Hour
	consulConfig.lockKey = "resec/.lock"
	consulConfig.lockMonitorRetryInterval = time.Second
	consulConfig.lockSessionName = "resec"
	consulConfig.lockMonitorRetries = 3
	consulConfig.lockTTL = 15 * time.Second
	consulConfig.tags = make(map[string][]string)
	consulConfig.serviceNamePrefix = "redis"

	if consulServiceName := os.Getenv(ConsulServiceName); consulServiceName != "" {
		consulConfig.serviceName = consulServiceName
	} else if consulServicePrefix := os.Getenv(ConsulServicePrefix); consulServicePrefix != "" {
		consulConfig.serviceNamePrefix = consulServicePrefix
	}

	// Fail if CONSUL_SERVICE_NAME is used and no MASTER_TAGS are provided
	if masterTags := os.Getenv(MasterTags); masterTags != "" {
		consulConfig.tags["master"] = strings.Split(masterTags, ",")
	} else if consulConfig.serviceName != "" {
		return nil, fmt.Errorf("[ERROR] MASTER_TAGS is required when CONSUL_SERVICE_NAME is used")
	}

	if slaveTags := os.Getenv(SlaveTags); slaveTags != "" {
		consulConfig.tags["slave"] = strings.Split(slaveTags, ",")
	}

	if consulConfig.serviceName != "" {
		if len(consulConfig.tags["slave"]) >= 1 && len(consulConfig.tags["master"]) >= 1 {
			if consulConfig.tags["slave"][0] == consulConfig.tags["master"][0] {
				return nil, fmt.Errorf("[PANIC] The first tag in %s and %s must be unique", MasterTags, SlaveTags)
			}
		}
	}

	if consulLockKey := os.Getenv(ConsulLockKey); consulLockKey != "" {
		consulConfig.lockKey = consulLockKey
	}

	if consulLockSessionName := os.Getenv(ConsulLockSessionName); consulLockSessionName != "" {
		consulConfig.lockSessionName = consulLockSessionName
	}

	if consulLockMonitorRetries := os.Getenv(ConsulLockMonitorRetries); consulLockMonitorRetries != "" {
		consulLockMonitorRetriesInt, err := strconv.Atoi(consulLockMonitorRetries)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as int: %s", ConsulLockMonitorRetries, consulLockMonitorRetries, err)
		}

		consulConfig.lockMonitorRetries = consulLockMonitorRetriesInt
	}

	if consulLockMonitorRetryInterval := os.Getenv(ConsulLockMonitorRetryInterval); consulLockMonitorRetryInterval != "" {
		consulLockMonitorRetryIntervalDuration, err := time.ParseDuration(consulLockMonitorRetryInterval)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", ConsulLockMonitorRetryInterval, consulLockMonitorRetryInterval, err)
		}

		consulConfig.lockMonitorRetryInterval = consulLockMonitorRetryIntervalDuration
	}

	// setting Consul Check TTL to be 2 * Check Interval
	consulConfig.ttl = 2 * config.healthCheckInterval

	if consulDeregisterServiceAfter := os.Getenv(ConsulDeregisterServiceAfter); consulDeregisterServiceAfter != "" {
		consulDeregisterServiceAfterDuration, err := time.ParseDuration(consulDeregisterServiceAfter)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", ConsulDeregisterServiceAfter, consulDeregisterServiceAfter, err)
		}

		consulConfig.deregisterServiceAfter = consulDeregisterServiceAfterDuration

	}

	if consuLockTTL := os.Getenv(ConsulLockTTL); consuLockTTL != "" {
		consuLockTTLDuration, err := time.ParseDuration(consuLockTTL)
		if err != nil {
			return nil, fmt.Errorf("[ERROR] Trouble parsing %s [%s] as time: %s", ConsulLockTTL, consuLockTTL, err)
		}

		if consuLockTTLDuration < time.Second*15 {
			return nil, fmt.Errorf("[CRITICAL] Minimum Consul lock session TTL is 15s")
		}

		consulConfig.lockTTL = consuLockTTLDuration
	}

	announceAddr := os.Getenv(AnnounceAddr)
	if announceAddr == "" {
		redisHost := strings.Split(redisConfig.address, ":")[0]
		redisPort := strings.Split(redisConfig.address, ":")[1]
		if redisHost == "127.0.0.1" || redisHost == "localhost" || redisHost == "::1" {
			consulConfig.announceAddr = ":" + redisPort
		} else {
			consulConfig.announceAddr = redisConfig.address
		}
	}

	var err error
	consulConfig.announceHost = strings.Split(consulConfig.announceAddr, ":")[0]
	consulConfig.announcePort, err = strconv.Atoi(strings.Split(consulConfig.announceAddr, ":")[1])
	if err != nil {
		return nil, fmt.Errorf("[ERROR] Trouble extracting port number from [%s]", redisConfig.address)
	}

	clientConfig := consulapi.DefaultConfig()
	connection := &consulConnection{}
	connection.logger = log.WithField("system", "consul")
	connection.stopCh = make(chan interface{}, 1)
	connection.masterCh = make(chan interface{}, 1)
	connection.config = consulConfig
	connection.clientConfig = clientConfig
	connection.client, err = consulapi.NewClient(connection.clientConfig)
	if err != nil {
		return nil, err
	}
	connection.state = &consulState{
		healthy: true,
	}
	connection.backoff = &backoff.Backoff{
		Min:    50 * time.Millisecond,
		Max:    10 * time.Second,
		Factor: 1.5,
		Jitter: false,
	}
	connection.stateCh = make(chan consulState)

	return connection, nil
}
