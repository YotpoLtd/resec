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

	rc.LockErrorCh, err = rc.lock.Lock(rc.lockAbortCh)
	rc.waitingForLock = false
	if err != nil {
		log.Printf("[ERROR] Failed to acquire lock - %s", err)
		return
	}

	if rc.lastRedisHealthCheckOK {
		log.Println("[INFO] Lock acquired")
		rc.consulLockIsHeld = true
		rc.promoteCh <- true
	}

}

func (rc *resecConfig) handleWaitForLockError() {
	for {
		err := <-rc.LockErrorCh
		rc.consulLockIsHeld = false
		log.Printf("[ERROR] Consul lock failed with error %s", err)
		log.Println("[INFO] Starting new waiter")
		//go rc.waitForLock()
	}
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
			log.Println("[DEBUG] Stopping waiting for consul lock")
			rc.lockAbortCh <- struct{}{}
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

	log.Printf("[INFO] Registed service [%s](id [%s]) with address [%s:%d]", serviceInfo.Name, serviceInfo.ID, serviceInfo.Address, serviceInfo.Port)

	return err
}

func (rc *resecConfig) Watch() error {
	params := map[string]interface{}{
		"type":        "service",
		"service":     rc.consulServiceNamePrefix + "-master",
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
			log.Printf("[DEBUG] got an array of ServiceEntry %s", masterConsulServiceStatus)
			masterCount := len(masterConsulServiceStatus)
			switch {
			case masterCount > 1:
				log.Printf("[DEBUG] Found more than one master registered in Consul.")
			case masterCount == 0:
				log.Printf("[DEBUG] No redis master services in Consul.")
			default:
				log.Printf("[DEBUG] Found redis master in Consul, %s", masterConsulServiceStatus[0])
				rc.masterConsulServiceCh <- masterConsulServiceStatus[0]
			}
		default:
			log.Printf("[ERROR] Got an unknown interface from Consul Watch %s", masterConsulServiceStatus)
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
