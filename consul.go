package main

import (
	"fmt"
	"log"
	"strings"

	consulapi "github.com/hashicorp/consul/api"
	consulwatch "github.com/hashicorp/consul/watch"
)

var (
	consulClient *consulapi.Client
)

// acquireConsulLeadership waits for lock to the Consul KV key.
// This will ensure we are the only master is holding a lock and registered
func (rc *resec) acquireConsulLeadership() {
	if rc.consul.lockIsHeld {
		log.Printf("[DEBUG] Lock is already held")
		return
	}

	if rc.consul.lockIsWaiting {
		log.Printf("[DEBUG] Already waiting for lock")
		return
	}

	var err error
	rc.consul.lockIsWaiting = true

	log.Printf("[INFO] Trying to acquire consul leadership")

	// create the consul client if it doesn't exist
	if consulClient == nil {
		consulClient, err = consulapi.NewClient(consulapi.DefaultConfig())
		if err != nil {
			rc.handleConsulError(err)
			return
		}
	}

	rc.consul.lock, err = consulClient.LockOpts(rc.consulLockOptions())
	if err != nil {
		log.Printf("[ERROR] Failed setting lock options - %s", err)
		rc.consul.lockStatusCh <- &consulLockStatus{
			acquired: false,
			err:      err,
		}
		rc.consul.lockIsWaiting = false
		return
	}

	rc.consul.lockIsWaiting = false
	rc.consul.lockErrorCh, err = rc.consul.lock.Lock(rc.consul.lockAbortCh)
	if err != nil {
		rc.consul.lockStatusCh <- &consulLockStatus{
			acquired: false,
			err:      err,
		}
		return
	}

	// we acquired the lock

	log.Println("[INFO] Lock acquired")
	rc.consul.lockIsWaiting = false
	rc.consul.lockIsHeld = true
	rc.consul.lockStatusCh <- &consulLockStatus{
		acquired: true,
		err:      nil,
	}

	go rc.handleWaitForLockError()
}

// handleWaitForLockError will wait for the consul lock to close
// meaning we've lost the lock and should step down as leader in our internal
// state as well
func (rc *resec) handleWaitForLockError() {
	log.Printf("[DEBUG] Starting Consul Lock Error Handler")

	rc.consul.lockWaitHandlerRunning = true
	rc.consul.lockStopWaiterHandlerCh = make(chan bool)

	select {
	case data, ok := <-rc.consul.lockErrorCh:
		if ok {
			log.Printf("[DEBUG] something wrote to lock error channel %v ", data)
			break
		}

		log.Printf("[DEBUG] Lock Error chanel is closed")

		err := fmt.Errorf("Consul lock lost or error")
		log.Printf("[DEBUG] %s", err)

		rc.consul.lockIsWaiting = false
		rc.consul.lockIsHeld = false

		rc.consul.lockStatusCh <- &consulLockStatus{
			acquired: false,
			err:      err,
		}

	case <-rc.consul.lockStopWaiterHandlerCh:
		// todo(jippi): should this not be set when the function returns
		//              like when lockErrorCh is closed
		rc.consul.lockWaitHandlerRunning = false
		log.Printf("[DEBUG] Stopped Consul Lock Error handler")
	}
}

// releaseConsulLock stops consul lock handler")
func (rc *resec) releaseConsulLock() {
	if rc.consul.lockWaitHandlerRunning {
		log.Printf("[DEBUG] Stopping Consul Lock Error handler")
		close(rc.consul.lockStopWaiterHandlerCh)
	}

	if rc.consul.lockIsHeld {
		log.Println("[DEBUG] Lock is held, releasing")
		err := rc.consul.lock.Unlock()
		// todo(jippi): don't we need to panic() here, our internal state is likely broken at this time
		//              and restarting will let us either re-acquire the lock or get back in sync
		if err != nil {
			log.Println("[ERROR] Can't release consul lock", err)
			return
		}

		rc.consul.lockIsHeld = false
		return
	}

	if rc.consul.lockIsWaiting {
		log.Printf("[DEBUG] Stopping wait for consul lock")
		rc.consul.lockAbortCh <- struct{}{}
		rc.consul.lockIsWaiting = false
		log.Printf("[INFO] Stopped wait for consul lock")
	}
}

// registerService registers a service in consul
func (rc *resec) registerService() error {
	nameToRegister := rc.consul.serviceName

	if nameToRegister == "" {
		nameToRegister = rc.consul.serviceNamePrefix + "-" + rc.redis.replicationStatus
	}

	rc.consul.serviceID = nameToRegister + ":" + rc.redis.address
	rc.consul.checkID = rc.consul.serviceID + ":replication-status-check"

	serviceInfo := &consulapi.AgentServiceRegistration{
		ID:   rc.consul.serviceID,
		Port: rc.announcePort,
		Name: nameToRegister,
		Tags: rc.consul.tags[rc.redis.replicationStatus],
	}

	if rc.announceHost != "" {
		serviceInfo.Address = rc.announceHost
	}

	log.Printf("[DEBUG] Registering %s service in consul", serviceInfo.Name)

	if err := rc.consul.client.Agent().ServiceRegister(serviceInfo); err != nil {
		rc.handleConsulError(err)
		return err
	}

	log.Printf("[INFO] Registered service [%s](id [%s]) with address [%s:%d]", serviceInfo.Name, serviceInfo.ID, serviceInfo.Address, serviceInfo.Port)
	log.Printf("[DEBUG] Adding TTL Check with id %s to service %s with id %s", rc.consul.checkID, nameToRegister, serviceInfo.ID)

	check := &consulapi.AgentCheckRegistration{
		Name:      rc.redis.replicationStatus + " replication status",
		ID:        rc.consul.checkID,
		ServiceID: rc.consul.serviceID,
		AgentServiceCheck: consulapi.AgentServiceCheck{
			TTL:    rc.consul.ttl,
			Status: "critical",
			DeregisterCriticalServiceAfter: rc.consul.deregisterServiceAfter.String(),
		},
	}
	if err := rc.consul.client.Agent().CheckRegister(check); err != nil {
		log.Println("[ERROR] Consul Check registration failed", "error", err)
		rc.handleConsulError(err)
		return err
	}

	log.Printf("[DEBUG] TTL Check added with id %s to service %s with id %s", rc.consul.checkID, nameToRegister, serviceInfo.ID)
	return nil
}

// setConsulCheckStatus sets consul status check
func (rc *resec) setConsulCheckStatus(output, status string) error {
	return rc.consul.client.Agent().UpdateTTL(rc.consul.checkID, output, status)
}

// watchConsulMasterService starts watching the service (+ tags) that represents the
// redis master service. All changes will be emitted to masterConsulServiceCh.
func (rc *resec) watchConsulMasterService() error {
	params := map[string]interface{}{
		"type":        "service",
		"service":     rc.consul.serviceNamePrefix + "-master",
		"passingonly": true,
	}

	if rc.consul.serviceName != "" {
		params["service"] = rc.consul.serviceName
		params["tag"] = rc.consul.tags["master"][0]
	}

	wp, err := consulwatch.Parse(params)
	if err != nil {
		log.Println("[ERROR] couldn't create a watch plan", "error", err)
		return err
	}

	wp.Handler = func(idx uint64, data interface{}) {
		switch masterConsulServiceStatus := data.(type) {
		case []*consulapi.ServiceEntry:
			log.Printf("[INFO] Received update for master from consul")
			rc.consulMasterServiceCh <- masterConsulServiceStatus

		default:
			log.Printf("[ERROR] Got an unknown interface from Consul %s", masterConsulServiceStatus)
		}
	}

	if err := wp.Run(rc.consul.clientConfig.Address); err != nil {
		log.Printf("[ERROR] Error watching for %s changes %s", params["service"], err)
	}

	return nil
}

func (rc *resec) consulLockOptions() *consulapi.LockOptions {
	return &consulapi.LockOptions{
		Key:         rc.consul.lockKey,
		SessionName: rc.consul.lockSessionName,
		SessionTTL:  rc.consul.lockTTL.String(),
	}
}

// handleConsulError is the error handler
func (rc *resec) handleConsulError(err error) {
	if err == nil {
		return
	}

	if strings.Contains(err.Error(), "dial tcp") {
		rc.consul.healthy = false
		log.Printf("[ERROR] Consul Agent is down")
		return
	}

	log.Printf("[ERROR] Consul error: %v", err)
}
