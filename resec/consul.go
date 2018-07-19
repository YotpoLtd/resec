package resec

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	consulwatch "github.com/hashicorp/consul/watch"
	log "github.com/sirupsen/logrus"
)

type consulConnection struct {
	logger       *log.Entry
	client       *consulapi.Client
	clientConfig *consulapi.Config
	config       *consulConfig
	state        *consulState
	stateCh      chan consulState
	lockCh       <-chan struct{}
	lockErrorCh  <-chan struct{}
	stopCh       chan interface{}
}

// Consul state represent the full state of the connection with Consul
type consulState struct {
	ready      bool
	err        error
	lockIsHeld bool
	masterAddr string
	masterPort int
}

type consulConfig struct {
	shuttingDown             bool
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
	lockStopWaiterHandlerCh  chan interface{}
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

func (cc *consulConnection) emit(err error) {
	cc.state.err = err
	cc.stateCh <- *cc.state
}

func (cc *consulConnection) cleanup() {
	cc.config.shuttingDown = true

	cc.logger.Debug("Releasing lock")
	cc.releaseConsulLock()

	cc.logger.Debug("Deregister service")
	cc.deregisterService()

	cc.logger.Debug("Closing stopCh")
	close(cc.stopCh)
}

// acquireConsulLeadership waits for lock to the Consul KV key.
// it will run until the stopCh is closed
func (cc *consulConnection) acquireConsulLeadership() {
	t := time.NewTicker(250 * time.Millisecond)

	for {
		select {
		case <-cc.stopCh:
			return

		case <-t.C:
			if cc.state.lockIsHeld {
				cc.logger.Debug("Lock is already held")
				continue
			}

			var err error

			cc.logger.Info("Trying to acquire consul leadership")
			cc.config.lock, err = cc.client.LockOpts(cc.consulLockOptions())
			if err != nil {
				cc.logger.Error("Failed setting lock options - %s", err)
				continue
			}

			cc.lockErrorCh, err = cc.config.lock.Lock(cc.lockCh)
			if err != nil {
				if !strings.Contains(err.Error(), "Client.Timeout exceeded while awaiting headers") {
					cc.logger.Errorf("Failed getting lock - %s", err)
				}
				continue
			}

			cc.logger.Info("Lock acquired")

			cc.state.lockIsHeld = true
			cc.emit(nil)

			cc.handleWaitForLockError()
		}
	}
}

// handleWaitForLockError will wait for the consul lock to close
// meaning we've lost the lock and should step down as leader in our internal
// state as well
func (cc *consulConnection) handleWaitForLockError() {
	cc.logger.Debug("Starting Consul Lock Error Handler")

	cc.config.lockStopWaiterHandlerCh = make(chan interface{})

	select {
	case <-cc.stopCh:
		cc.state.lockIsHeld = false
		cc.emit(nil)

	case data, ok := <-cc.lockErrorCh:
		if ok {
			cc.logger.Debugf("Something wrote to lock error channel %v ", data)
			break
		}

		cc.logger.Debug("Lock Error channel is closed")

		err := fmt.Errorf("Consul lock lost or error")
		cc.logger.Error(err)

		cc.state.lockIsHeld = false
		cc.emit(err)

	case <-cc.config.lockStopWaiterHandlerCh:
		cc.logger.Debugf("Stopped Consul Lock Error handler")
		return
	}
}

// releaseConsulLock stops consul lock handler")
func (cc *consulConnection) releaseConsulLock() {
	if cc.state.lockIsHeld == false {
		cc.logger.Debug("Lock is not held, nothing to release")
		return
	}

	cc.logger.Debug("Lock is held, releasing")

	err := cc.config.lock.Unlock()
	if err != nil {
		cc.logger.Errorf("Can't release consul lock: %v", err)
		return
	}
	close(cc.config.lockStopWaiterHandlerCh)
	cc.logger.Debug("lock released!")

	cc.state.lockIsHeld = false
	cc.emit(nil)
}

// registerService registers a service in consul
func (cc *consulConnection) registerService(redisState redisState) error {
	replicationStatus := "slave"
	if cc.state.lockIsHeld {
		replicationStatus = "master"
	}

	nameToRegister := cc.config.serviceName

	if nameToRegister == "" {
		nameToRegister = cc.config.serviceNamePrefix + "-" + replicationStatus
	}

	cc.config.serviceID = nameToRegister + ":" + strconv.Itoa(cc.config.announcePort)
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

	if err := cc.client.Agent().ServiceRegister(serviceInfo); err != nil {
		cc.handleConsulError(err)
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
			TTL:    cc.config.ttl.String(),
			Status: "passing",
			DeregisterCriticalServiceAfter: cc.config.deregisterServiceAfter.String(),
		},
	}
	if err := cc.client.Agent().CheckRegister(check); err != nil {
		cc.logger.Errorf("Consul Check registration failed: %s", err)
		cc.handleConsulError(err)
		return err
	}

	cc.logger.Debugf("TTL Check added with id %s to service %s with id %s", cc.config.checkID, nameToRegister, serviceInfo.ID)
	return nil
}

func (cc *consulConnection) deregisterService() {
	if err := cc.client.Agent().ServiceDeregister(cc.config.serviceID); err != nil {
		cc.handleConsulError(err)
		cc.logger.Error("Can't deregister consul service, %s", err)
	}
}

// setConsulCheckStatus sets consul status check
func (cc *consulConnection) setConsulCheckStatus(output, status string) error {
	return cc.client.Agent().UpdateTTL(cc.config.checkID, output, status)
}

// watchConsulMasterService starts watching the service (+ tags) that represents the
// redis master service. All changes will be emitted to masterConsulServiceCh.
func (cc *consulConnection) watchConsulMasterService() error {
	params := map[string]interface{}{
		"type":        "service",
		"service":     cc.config.serviceNamePrefix + "-master",
		"passingonly": true,
	}

	if cc.config.serviceName != "" {
		params["service"] = cc.config.serviceName
		params["tag"] = cc.config.tags["master"][0]
	}

	wp, err := consulwatch.Parse(params)
	if err != nil {
		cc.logger.Errorf("Couldn't create a watch plan: %s", err)
		return err
	}

	wp.Handler = func(idx uint64, data interface{}) {
		masterConsulServiceStatus, ok := data.([]*consulapi.ServiceEntry)
		if !ok {
			cc.logger.Errorf("Got an unknown interface from Consul: %T", masterConsulServiceStatus)
			return
		}

		if len(masterConsulServiceStatus) == 0 {
			cc.logger.Info("0 master services found in Consul catalog")
			return
		}

		if len(masterConsulServiceStatus) > 1 {
			cc.logger.Warn("More than 1 master service found in Consul catalog")
			return
		}

		master := masterConsulServiceStatus[0]

		cc.state.masterAddr = master.Node.Address
		cc.state.masterPort = master.Service.Port
		cc.emit(nil)
	}

	if err := wp.Run(cc.clientConfig.Address); err != nil {
		log.Printf("[ERROR] Error watching for %s changes %s", params["service"], err)
	}

	return nil
}

func (cc *consulConnection) consulLockOptions() *consulapi.LockOptions {
	return &consulapi.LockOptions{
		Key:              cc.config.lockKey,
		SessionName:      cc.config.lockSessionName,
		SessionTTL:       cc.config.lockTTL.String(),
		MonitorRetries:   cc.config.lockMonitorRetries,
		MonitorRetryTime: cc.config.lockMonitorRetryInterval,
	}
}

// handleConsulError is the error handler
func (cc *consulConnection) handleConsulError(err error) {
	if err == nil {
		return
	}

	if strings.Contains(err.Error(), "dial tcp") {
		cc.logger.Error("[ERROR] Consul Agent is down")
	}

	cc.logger.Error("Consul error: %v", err)
}

func (cc *consulConnection) loop() {
	go cc.acquireConsulLeadership()
	go cc.watchConsulMasterService()

	t := time.NewTicker(250 * time.Millisecond)

	for {
		select {
		case <-cc.stopCh:
			return

		case <-t.C:
			if cc.config.shuttingDown {
				cc.logger.Info("Stopping Consul worker loop, shutting down ...")
				return
			}
		}
	}
}

func (cc *consulConnection) start() {
	go cc.loop()

	cc.state.ready = true
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
	clientConfig.HttpClient = &http.Client{
		Timeout: 1 * time.Second,
	}

	connection := &consulConnection{}
	connection.logger = log.WithField("system", "consul")
	connection.stopCh = make(chan interface{}, 1)
	connection.config = consulConfig
	connection.clientConfig = clientConfig
	connection.client, err = consulapi.NewClient(connection.clientConfig)
	if err != nil {
		return nil, err
	}
	connection.state = &consulState{}
	connection.stateCh = make(chan consulState)

	return connection, nil
}
