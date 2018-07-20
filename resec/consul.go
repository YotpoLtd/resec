package resec

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/jpillora/backoff"
	log "github.com/sirupsen/logrus"
	"gopkg.in/urfave/cli.v1"
)

type consulConnection struct {
	backoff      *backoff.Backoff  // exponential backoff helper
	client       *consulapi.Client // consul API client
	clientConfig *consulapi.Config // consul API client configuration
	config       *consulConfig     // configuration for this connection
	lockCh       <-chan struct{}   // lock channel used by Consul SDK to notify about changes
	lockErrorCh  <-chan struct{}   // lock error channel used by Consul SDK to notify about errors related to the lock
	logger       *log.Entry        // logger for the consul connection struct
	masterCh     chan interface{}  // notification channel used to notify the Consul Lock go-routing that the master service changed
	state        *consulState      // state used by the reconciler
	stateCh      chan consulState  // state channel used to notify the reconciler of changes
	stopCh       chan interface{}  // internal channel used to stop all go-routines when gracefully shutting down
}

// Consul state used by the reconciler to decide what actions to take
type consulState struct {
	ready      bool
	healthy    bool
	lockIsHeld bool
	masterAddr string
	masterPort int
}

// Consul config used for internal state management
type consulConfig struct {
	announceAddr             string              // address (IP:port) to announce to Consul
	announceHost             string              // host (IP) to announce to Consul
	announcePort             int                 // port to announce to Consul
	checkID                  string              // consul check ID
	deregisterServiceAfter   time.Duration       // after how long time in warnign should service be auto-deregistered
	lock                     *consulapi.Lock     // Consul Lock instance
	lockKey                  string              // Consul KV key to lock
	lockMonitorRetries       int                 // How many times should we retry to reclaim the lock if we lose connectivity to Consul / leader election
	lockMonitorRetryInterval time.Duration       // How long to wait between retries
	lockSessionName          string              // Name of the session that we use to claim the lock
	voluntarilyReleaseLockCh chan interface{}    // Release the lock if this channel is closed (if we have it, otherwise NOOP)
	lockTTL                  time.Duration       // How frequently do we ned to keep-alive our lock to have it not expire
	serviceID                string              // Consul service ID
	serviceName              string              // Consul service Name
	serviceNamePrefix        string              // Consul service name (prefix)
	serviceTagsByRole        map[string][]string // Tags to include for a service, depending on the role we have
	serviceTTL               time.Duration
}

// emit will emit a consul state change to the reconciler
func (cc *consulConnection) emit() {
	cc.stateCh <- *cc.state
}

// cleanup will do cleanup tasks when the reconciler is shutting down
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
			if cc.state.healthy == false {
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
func (cc *consulConnection) acquireConsulLeadership() {
	// if we already hold the lock, we can't acquire it again
	if cc.state.lockIsHeld {
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

	cc.state.lockIsHeld = true
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

		cc.state.lockIsHeld = false
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
func (cc *consulConnection) releaseConsulLock() {
	if cc.state.lockIsHeld == false {
		cc.logger.Debug("Can't release Consul lock, we don't have it")
		return
	}

	cc.logger.Info("Releasing Consul lock")
	close(cc.config.voluntarilyReleaseLockCh)
}

// getReplicationStatus will return current replication status
// the default value is 'slave' - only if we hold the lock will 'master' be returned
func (cc *consulConnection) getReplicationStatus() string {
	if cc.state.lockIsHeld {
		return "master"
	}

	return "slave"
}

// getConsulServiceName will return the consul service name to use
// depending on using tagged service (replication status as tag) or
// service name (with replication status as suffix)
func (cc *consulConnection) getConsulServiceName() string {
	name := cc.config.serviceName

	if name == "" {
		name = cc.config.serviceNamePrefix + "-" + cc.getReplicationStatus()
	}

	return name
}

// getConsulServiceID will return the consul service ID
// it will either use the service name, or the service name prefix depending
// on your configuration
func (cc *consulConnection) getConsulServiceID() string {
	ID := cc.config.serviceName

	if cc.config.serviceName == "" {
		ID = cc.config.serviceNamePrefix
	}

	return ID
}

// registerService registers a service in consul
func (cc *consulConnection) registerService(redisState redisState) {
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
		serviceTag = cc.config.serviceTagsByRole["master"][0]
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
			cc.emit()

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
		cc.emit()

		return
	}

	// if we get connection refused, we are not healthy
	if cc.state.healthy && strings.Contains(err.Error(), "dial tcp") {
		cc.state.healthy = false
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

func (cc *consulConnection) start() {
	go cc.continuouslyAcquireConsulLeadership()
	go cc.watchConsulMasterService()

	cc.state.ready = true
	cc.emit()
}

func newConsulConnection(c *cli.Context, redisConfig *redisConfig) (*consulConnection, error) {
	consulConfig := &consulConfig{
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
		return nil, fmt.Errorf("Trouble extracting port number from [%s]", redisConfig.address)
	}

	connection := &consulConnection{
		backoff: &backoff.Backoff{
			Min:    50 * time.Millisecond,
			Max:    10 * time.Second,
			Factor: 1.5,
			Jitter: false,
		},
		clientConfig: consulapi.DefaultConfig(),
		config:       consulConfig,
		logger:       log.WithField("system", "consul"),
		masterCh:     make(chan interface{}, 1),
		stateCh:      make(chan consulState),
		stopCh:       make(chan interface{}, 1),
		state: &consulState{
			healthy: true,
		},
	}

	connection.client, err = consulapi.NewClient(connection.clientConfig)
	if err != nil {
		return nil, err
	}

	return connection, nil
}
