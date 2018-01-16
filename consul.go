package main

import (
	"log"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	consulwatch "github.com/hashicorp/consul/watch"
)

func (rc *resecConfig) consulClientInit() {
	var err error
	rc.consulClient, err = consulapi.NewClient(rc.consulClientConfig)

	if err != nil {
		log.Fatalf("[CRITICAL] Can't initialize consul client %s", err)
	}
}

// Wait for lock to the Consul KV key.
// This will ensure we are the only master is holding a lock and registered
func (rc *resecConfig) waitForLock() {
	go rc.handleWaitForLockError()

	rc.waitingForLock = true
	log.Println("[INFO] Trying to acquire leader lock")

	var err error

	rc.lock, err = rc.consulClient.LockOpts(&consulapi.LockOptions{
		Key:         rc.consulLockKey,
		SessionName: "resec",
		SessionTTL:  "15s",
	})

	if err != nil {
		log.Printf("[ERROR] Failed setting lock options - %s", err)
		rc.waitingForLock = false
		return
	}

	if rc.LockErrorCh == nil {
		log.Println("here")
	}

	rc.LockErrorCh, err = rc.lock.Lock(rc.lockAbortCh)
	rc.waitingForLock = false
	if err != nil {
		log.Printf("[ERROR] Failed to acquire lock - %s", err)
		return
	}

	if rc.LockErrorCh != nil {
		log.Println("[INFO] Lock acquired")
		rc.consulLockIsHeld = true
		rc.promoteCh <- true
	}

}

func (rc *resecConfig) handleWaitForLockError() {
	err := <-rc.LockErrorCh
	rc.consulLockIsHeld = false
	log.Printf("[ERROR] Consul lock failed with error %s", err)
	// rc.waitForLock() // ifi rerun waiter - it opens gazillion sessions
}

func (rc *resecConfig) AbortConsulLock() {
	if rc.consulLockIsHeld {
		log.Println("[DEBUG] Lock is held, releasing")
		err := rc.lock.Unlock()
		if err != nil {
			log.Println("[ERROR] Can't release consul lock", err)
		}
	} else {
		if rc.waitingForLock {
			log.Printf("[DEBUG] Stopping wait for consul lock")
			rc.lockAbortCh <- struct{}{}
			log.Printf("[INFO] Stopped wait for consul lock")

		}
	}

}

func (rc *resecConfig) serviceRegister(replication_role string) error {

	nameToRegister := rc.consulServiceNamePrefix + "-" + replication_role
	rc.consulServiceId = nameToRegister + ":" + rc.redisAddr
	rc.consulCheckId = rc.consulServiceId + ":replication-status-check"

	serviceInfo := &consulapi.AgentServiceRegistration{
		ID:   rc.consulServiceId,
		Port: rc.announcePort,
		Name: nameToRegister,
	}

	if rc.announceHost != "" {
		serviceInfo.Address = rc.announceHost
	}

	log.Printf("[DEBUG] Registering %s service in consul", serviceInfo.Name)

	err := rc.consulClient.Agent().ServiceRegister(serviceInfo)
	if err != nil {
		log.Println("[ERROR] Consul Service registration failed", "error", err)
		return err
	}

	log.Printf("[INFO] Registed service [%s](id [%s]) with address [%s:%d]", serviceInfo.Name, serviceInfo.ID, serviceInfo.Address, serviceInfo.Port)

	log.Printf("[DEBUG] Adding TTL Check with id %s to service %s with id %s", rc.consulCheckId, nameToRegister, serviceInfo.ID)

	err = rc.consulClient.Agent().CheckRegister(&consulapi.AgentCheckRegistration{
		Name:      replication_role + " replication status",
		ID:        rc.consulCheckId,
		ServiceID: rc.consulServiceId,
		AgentServiceCheck: consulapi.AgentServiceCheck{
			TTL: rc.consulTTL,
			DeregisterCriticalServiceAfter: time.Duration(rc.healthCheckInterval * 10).String(),
		},
	})

	if err != nil {
		log.Println("[ERROR] Consul Check registration failed", "error", err)
		return err
	}

	return err
}

func (rc *resecConfig) watchForMaster() error {
	serviceToWatch := rc.consulServiceNamePrefix + "-master"
	params := map[string]interface{}{
		"type":        "service",
		"service":     serviceToWatch,
		"passingonly": true,
	}

	wp, err := consulwatch.Parse(params)
	if err != nil {
		log.Println("[ERROR] couldn't create a watch plan", "error", err)
		return err
	}

	wp.Handler = func(idx uint64, data interface{}) {
		switch masterConsulServiceStatus := data.(type) {
		case []*consulapi.ServiceEntry:
			log.Printf("[INFO] Received update for %s from consul", serviceToWatch)
			masterCount := len(masterConsulServiceStatus)
			switch {
			case masterCount > 1:
				log.Printf("[ERROR] Found more than one master registered in Consul")
			case masterCount == 0:
				log.Printf("[INFO] No redis master services in Consul")
			default:
				log.Printf("[INFO] Found redis master in Consul")
				rc.masterConsulServiceCh <- masterConsulServiceStatus[0]
			}
		default:
			log.Printf("[ERROR] Got an unknown interface from Consul %s", masterConsulServiceStatus)
		}

	}

	go func() {
		if err := wp.Run(rc.consulClientConfig.Address); err != nil {
			log.Printf("[ERROR] Error watching for %s changes %s", rc.consulServiceNamePrefix+"-master", err)
		}
	}()

	//Check if we should quit
	//wait forever for a stop signal to happen
	go func() {
		rc.masterWatchRunning = true
		<-rc.stopWatchCh
		rc.masterWatchRunning = false
		log.Printf("[INFO] Stopped watching for %s service changes", rc.consulServiceNamePrefix+"-master")
		wp.Stop()
	}()

	return nil
}

func (rc *resecConfig) stopWatchForMaster() {
	log.Printf("[DEBUG] Stopping watching for master changes")
	if rc.masterWatchRunning {
		//Stopping the watch for master
		rc.stopWatchCh <- struct{}{}
	}
}
