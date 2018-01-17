package main

import (
	"fmt"
	"log"

	consulapi "github.com/hashicorp/consul/api"
	consulwatch "github.com/hashicorp/consul/watch"
)

func (rc *resecConfig) ConsulClientInit() {
	var err error
	rc.consul.Client, err = consulapi.NewClient(rc.consul.ClientConfig)

	if err != nil {
		log.Fatalf("[CRITICAL] Can't initialize consul client %s", err)
	}

	leader, err := rc.consul.Client.Status().Leader()

	if err != nil {
		log.Fatalf("[CRITICAL] Consul: %s", err)
	} else {
		log.Printf("[DEBUG] Consul cluser is healthy, leader is %s ", leader)
	}
}

// Wait for lock to the Consul KV key.
// This will ensure we are the only master is holding a lock and registered
func (rc *resecConfig) WaitForLock() {
	go rc.handleWaitForLockError()

	rc.consul.LockIsWaiting = true
	log.Println("[INFO] Trying to acquire leader lock")

	var err error

	rc.consul.Lock, err = rc.consul.Client.LockOpts(&consulapi.LockOptions{
		Key:         rc.consul.LockKey,
		SessionName: "resec",
		SessionTTL:  rc.consul.LockTTL.String(),
	})

	if err != nil {
		log.Printf("[ERROR] Failed setting lock options - %s", err)
		rc.consul.LockIsWaiting = false
		rc.consul.LockStatus <- &ConsulLockStatus{
			Acquired: false,
			Error:    err,
		}
		return
	}

	rc.consul.LockErrorCh, err = rc.consul.Lock.Lock(rc.consul.LockAbortCh)
	rc.consul.LockIsWaiting = false
	if err != nil {
		log.Printf("[ERROR] Failed to acquire lock - %s", err)
		rc.consul.LockStatus <- &ConsulLockStatus{
			Acquired: false,
			Error:    err,
		}
		return
	}

	//if Lock Error Channel is not closed, means all good and we acquired lock
	if rc.consul.LockErrorCh != nil {
		log.Println("[INFO] Lock acquired")
		rc.consul.LockIsWaiting = false
		rc.consul.LockIsHeld = true
		rc.consul.LockStatus <- &ConsulLockStatus{
			Acquired: true,
			Error:    nil,
		}
	}
}

func (rc *resecConfig) handleWaitForLockError() {
	_, ok := <-rc.consul.LockErrorCh

	if !ok {
		ErrMessage := fmt.Errorf("Consul lock lost or error")
		log.Printf("[DEBUG] %s", ErrMessage)
		rc.consul.LockIsWaiting = false
		rc.consul.LockIsHeld = false
		rc.consul.LockStatus <- &ConsulLockStatus{
			Acquired: false,
			Error:    ErrMessage,
		}
	}
}

func (rc *resecConfig) AbortConsulLock() {
	if rc.consul.LockIsHeld {
		log.Println("[DEBUG] Lock is held, releasing")
		err := rc.consul.Lock.Unlock()
		if err != nil {
			log.Println("[ERROR] Can't release consul lock", err)
		}
		rc.consul.LockIsHeld = false
	} else {
		if rc.consul.LockIsWaiting {
			log.Printf("[DEBUG] Stopping wait for consul lock")
			rc.consul.LockAbortCh <- struct{}{}
			log.Printf("[INFO] Stopped wait for consul lock")

		}
	}

}

func (rc *resecConfig) ServiceRegister(replication_role string) error {

	nameToRegister := rc.consul.ServiceNamePrefix + "-" + replication_role
	rc.consul.ServiceId = nameToRegister + ":" + rc.redis.Addr
	rc.consul.CheckId = rc.consul.ServiceId + ":replication-status-check"

	serviceInfo := &consulapi.AgentServiceRegistration{
		ID:   rc.consul.ServiceId,
		Port: rc.announcePort,
		Name: nameToRegister,
	}

	if rc.announceHost != "" {
		serviceInfo.Address = rc.announceHost
	}

	log.Printf("[DEBUG] Registering %s service in consul", serviceInfo.Name)

	err := rc.consul.Client.Agent().ServiceRegister(serviceInfo)
	if err != nil {
		log.Println("[ERROR] Consul Service registration failed", "error", err)
		return err
	}

	log.Printf("[INFO] Registed service [%s](id [%s]) with address [%s:%d]", serviceInfo.Name, serviceInfo.ID, serviceInfo.Address, serviceInfo.Port)

	log.Printf("[DEBUG] Adding TTL Check with id %s to service %s with id %s", rc.consul.CheckId, nameToRegister, serviceInfo.ID)

	err = rc.consul.Client.Agent().CheckRegister(&consulapi.AgentCheckRegistration{
		Name:      replication_role + " replication status",
		ID:        rc.consul.CheckId,
		ServiceID: rc.consul.ServiceId,
		AgentServiceCheck: consulapi.AgentServiceCheck{
			TTL: rc.consul.TTL,
			DeregisterCriticalServiceAfter: rc.consul.DeregisterServiceAfter.String(),
		},
	})

	if err != nil {
		log.Println("[ERROR] Consul Check registration failed", "error", err)
		return err
	}

	log.Printf("[DEBUG] TTL Check added with id %s to service %s with id %s", rc.consul.CheckId, nameToRegister, serviceInfo.ID)

	return err
}

func (rc *resecConfig) SetConsulCheckStatus(output, status string) error {
	return rc.consul.Client.Agent().UpdateTTL(rc.consul.CheckId, output, status)
}

func (rc *resecConfig) WatchForMaster() error {
	serviceToWatch := rc.consul.ServiceNamePrefix + "-master"
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
			rc.masterConsulServiceCh <- masterConsulServiceStatus
		default:
			log.Printf("[ERROR] Got an unknown interface from Consul %s", masterConsulServiceStatus)
		}

	}

	go func() {
		if err := wp.Run(rc.consul.ClientConfig.Address); err != nil {
			log.Printf("[ERROR] Error watching for %s changes %s", rc.consul.ServiceNamePrefix+"-master", err)
		}
	}()

	return nil
}
