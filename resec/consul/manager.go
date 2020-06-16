package consul

import (
	"strings"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/seatgeek/resec/resec/state"
)

// emit will emit a consul state change to the reconciler
func (m *Manager) emit() {
	m.stateCh <- *m.state
}

// cleanup will do cleanup tasks when the reconciler is shutting down
func (m *Manager) cleanup() {
	m.logger.Debug("Releasing lock")
	m.releaseConsulLock()

	m.logger.Debug("Deregister service")
	m.deregisterService()

	m.logger.Debug("Closing stopCh")
	close(m.stopCh)

	m.state.Stopped = true
	m.emit()
}

// continuouslyAcquireConsulLeadership waits to acquire the lock to the Consul KV key.
// it will run until the stopCh is closed
func (m *Manager) continuouslyAcquireConsulLeadership() {
	interval := 250 * time.Millisecond
	timer := time.NewTimer(interval)

	for {
		select {
		// if closed, we should stop working
		case <-m.stopCh:
			return

		// Periodically try to acquire the consul lock
		case <-timer.C:
			m.acquireConsulLeadership()

			// if we are not healthy, apply exponential backoff
			if m.state.Healthy == false {
				backoffDuration := m.backoff.Duration()
				m.logger.Errorf("Consul is not healthy, going to apply backoff of %s until next attempt", backoffDuration.Round(time.Second).String())
				timer.Reset(backoffDuration)
				continue
			}

			timer.Reset(interval)
		}
	}
}

// acquireConsulLeadership will one-off try to acquire the consul lock needed to become
// redis Master node
func (m *Manager) acquireConsulLeadership() {
	// if we already hold the lock, we can't acquire it again
	if m.state.Master {
		m.logger.Debug("We already hold the lock, can't acquire it again")
		return
	}

	// create the lock structs
	lockOptions := &consulapi.LockOptions{
		Key:              m.config.lockKey,
		SessionName:      m.config.lockSessionName,
		SessionTTL:       m.config.lockTTL.String(),
		MonitorRetries:   m.config.lockMonitorRetries,
		MonitorRetryTime: m.config.lockMonitorRetryInterval,
	}

	// Create the Lock options
	{
		var err error

		m.config.lock, err = m.client.LockOpts(lockOptions)
		if err != nil {
			m.logger.Errorf("Failed create lock options: %+v", err)
			return
		}
	}

	// try to acquire the lock
	{
		var err error

		m.logger.Infof("Trying to acquire consul lock")
		m.lockErrorCh, err = m.config.lock.Lock(m.lockCh)
		m.handleConsulError(err)
		if err != nil {
			return
		}

		m.logger.Info("Lock successfully acquired")

		m.state.Master = true
		m.emit()
	}

	//
	// start monitoring the consul lock for errors / changes
	//

	m.config.voluntarilyReleaseLockCh = make(chan interface{})

	// At this point, if we return from this function, we need to make sure
	// we release the lock
	defer func() {
		err := m.config.lock.Unlock()
		m.handleConsulError(err)
		if err != nil {
			m.logger.Errorf("Could not release Consul Lock: %v", err)
		} else {
			m.logger.Info("Consul Lock successfully released")
		}

		m.state.Master = false
		m.emit()
	}()

	// Wait for changes to Consul Lock
	for {
		select {
		// Global stop of all go-routines, reconciler is shutting down
		case <-m.stopCh:
			return

		// Changes on the lock error channel
		// if the channel is closed, it mean that we no longer hold the lock
		// if written to, we simply pass on the message
		case data, ok := <-m.lockErrorCh:
			if !ok {
				m.logger.Error("Consul Lock error channel was closed, we no longer hold the lock")
				return
			}

			m.logger.Warnf("Something wrote to lock error channel %+v", data)

		// voluntarily release our claim on the lock
		case <-m.config.voluntarilyReleaseLockCh:
			m.logger.Warnf("Voluntarily releasing the Consul lock")
			return
		}
	}
}

// releaseConsulLock stops consul lock handler")
func (m *Manager) releaseConsulLock() {
	if m.state.Master == false {
		m.logger.Debug("Can't release Consul lock, we don't have it")
		return
	}

	m.logger.Info("Releasing Consul lock")
	close(m.config.voluntarilyReleaseLockCh)
}

// getReplicationStatus will return current replication status
// the default value is 'slave' - only if we hold the lock will 'master' be returned
func (m *Manager) getReplicationStatus() string {
	if m.state.Master {
		return "master"
	}

	return "slave"
}

// getConsulServiceName will return the consul service name to use
// depending on using tagged service (replication status as tag) or
// service name (with replication status as suffix)
func (m *Manager) getConsulServiceName() string {
	name := m.config.serviceName

	if name == "" {
		name = m.config.serviceNamePrefix + "-" + m.getReplicationStatus()
	}

	return name
}

// getConsulServiceID will return the consul service ID
// it will either use the service name, or the service name prefix depending
// on your configuration
func (m *Manager) getConsulServiceID() string {
	ID := m.config.serviceName

	if m.config.serviceName == "" {
		ID = m.config.serviceNamePrefix
	}

	return ID
}

// registerService registers a service in consul
func (m *Manager) registerService(redisState state.Redis) {
	serviceID := m.getConsulServiceID()
	replicationStatus := m.getReplicationStatus()

	m.config.serviceID = serviceID + "@" + m.config.announceAddr
	m.config.checkID = m.config.serviceID + ":replication-status-check"

	serviceInfo := &consulapi.AgentServiceRegistration{
		ID:   m.config.serviceID,
		Port: m.config.announcePort,
		Name: m.getConsulServiceName(),
		Tags: m.config.serviceTagsByRole[replicationStatus],
	}

	if m.config.announceHost != "" {
		serviceInfo.Address = m.config.announceHost
	}

	m.logger.Infof("Registering %s service in consul", serviceInfo.Name)

	err := m.client.ServiceRegister(serviceInfo)
	m.handleConsulError(err)
	if err != nil {
		return
	}

	m.logger.Infof("Registered service %s (%s) with address %s:%d", serviceInfo.Name, serviceInfo.ID, serviceInfo.Address, serviceInfo.Port)

	check := &consulapi.AgentCheckRegistration{
		Name:      "Resec: " + replicationStatus + " replication status",
		ID:        m.config.checkID,
		ServiceID: m.config.serviceID,
		AgentServiceCheck: consulapi.AgentServiceCheck{
			TTL:                            m.config.serviceTTL.String(),
			Status:                         "warning",
			DeregisterCriticalServiceAfter: m.config.deregisterServiceAfter.String(),
		},
	}

	err = m.client.CheckRegister(check)
	m.handleConsulError(err)
}

// deregisterService will deregister the consul service from Consul catalog
func (m *Manager) deregisterService() {
	if m.state.Healthy && m.config.serviceID != "" {
		err := m.client.ServiceDeregister(m.config.serviceID)
		m.handleConsulError(err)
	}

	m.config.serviceID = ""
	m.config.checkID = ""
}

// setConsulCheckStatus sets consul status check TTL and output
func (m *Manager) setConsulCheckStatus(redisState state.Redis) {
	if m.config.checkID == "" {
		m.registerService(redisState)
	}

	m.logger.Debug("Updating Check TTL for service")
	err := m.client.UpdateTTL(m.config.checkID, redisState.InfoString, "passing")
	m.handleConsulError(err)
}

// getConsulMasterDetails will return the Consul service name and tag
// that matches what the Resec acting as master will expose in Consul
func (m *Manager) getConsulMasterDetails() (serviceName string, serviceTag string) {
	serviceName = m.config.serviceNamePrefix + "-master"

	if m.config.serviceName != "" {
		serviceName = m.config.serviceName
		serviceTag = m.config.serviceTagsByRole["master"][0]
	}

	return serviceName, serviceTag
}

// watchConsulMasterService starts watching the service (+ tags) that represents the
// redis master service. All changes will be emitted to masterConsulServiceCh.
func (m *Manager) watchConsulMasterService() {
	serviceName, serviceTag := m.getConsulMasterDetails()

	q := &consulapi.QueryOptions{
		WaitIndex: 1,
		WaitTime:  30 * time.Second,
	}

	// How often we should force a refresh of external state?
	duration := 250 * time.Millisecond
	timer := time.NewTimer(duration)

	for {
		select {

		case <-m.stopCh:
			return

		case <-timer.C:
			services, meta, err := m.client.ServiceHealth(serviceName, serviceTag, true, q)
			m.handleConsulError(err)
			if err != nil {
				timer.Reset(m.backoff.ForAttempt(m.backoff.Attempt()))
				continue
			}

			timer.Reset(duration)

			if q.WaitIndex == meta.LastIndex {
				m.logger.Debugf("No change in master service health")
				continue
			}

			q.WaitIndex = meta.LastIndex

			if len(services) == 0 {
				m.logger.Warn("No (healthy) master service found in Consul catalog")
				continue
			}

			if len(services) > 1 {
				m.logger.Error("More than 1 (healthy) master service found in Consul catalog")
				continue
			}

			master := services[0]

			if m.state.MasterAddr == master.Service.Address && m.state.MasterPort == master.Service.Port {
				m.logger.Debugf("No change in master service configuration")
				continue
			}

			// handle If master is registred in consul with port only
			// Use node IP insted of service IP
			if master.Service.Address != "" {
				m.state.MasterAddr = master.Service.Address
			} else {
				m.state.MasterAddr = master.Node.Address
			}

			m.state.MasterPort = master.Service.Port
			m.emit()
		}
	}
}

// handleConsulError is the error handler
func (m *Manager) handleConsulError(err error) {
	// if no error
	if err == nil {
		// if state is healthy do nothing
		if m.state.Healthy {
			return
		}

		// reset exponential backoff
		m.backoff.Reset()

		// mark us as healthy
		m.state.Healthy = true
		m.emit()

		return
	}

	// if we get connection refused, we are not healthy
	if m.state.Healthy && strings.Contains(err.Error(), "dial tcp") {
		m.state.Healthy = false
		m.emit()

		m.deregisterService()
	}

	// if the check don't have a TTL, it mean that the service + check is gone
	// deregister the service+check so we can register a new one
	if strings.Contains(err.Error(), "does not have associated TTL") {
		m.deregisterService()
	}

	m.logger.Errorf("Consul error: %v", err)
}

func (m *Manager) CommandRunner() {
	for {
		select {

		case <-m.stopCh:
			return

		case payload := <-m.commandCh:
			switch payload.name {
			case RegisterServiceCommand:
				m.registerService(payload.redisState)

			case DeregisterServiceCommand:
				m.deregisterService()

			case UpdateServiceCommand:
				m.setConsulCheckStatus(payload.redisState)

			case ReleaseLockCommand:
				m.releaseConsulLock()

			case StartCommand:
				m.start()

			case StopConsulCommand:
				m.cleanup()
			}
		}
	}
}

func (m *Manager) start() {
	go m.continuouslyAcquireConsulLeadership()
	go m.watchConsulMasterService()

	m.state.Ready = true
	m.emit()
}

func (m *Manager) GetStateReader() <-chan state.Consul {
	return m.stateCh
}

func (m *Manager) GetCommandWriter() chan<- Command {
	return m.commandCh
}
