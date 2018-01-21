package main

import (
	"fmt"
	"log"
	"strings"

	consulapi "github.com/hashicorp/consul/api"
	consulwatch "github.com/hashicorp/consul/watch"
)

// Wait for lock to the Consul KV key.
// This will ensure we are the only master is holding a lock and registered
func (rc *Resec) TryWaitForLock() {
	if !rc.consul.LockIsWaiting {
		log.Printf("[DEBUG] Not waiting for lock")
		if !rc.consul.LockIsHeld {
			log.Printf("[DEBUG] Lock is not held")

			rc.consul.LockIsWaiting = true
			log.Println("[INFO] Trying to acquire leader lock")

			var err error

			consulNoTimeOutClient, err := consulapi.NewClient(consulapi.DefaultConfig())
			if err != nil {
				rc.HandleConsulError(err)
			}

			rc.consul.Lock, err = consulNoTimeOutClient.LockOpts(&consulapi.LockOptions{
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
				rc.consul.LockStatus <- &ConsulLockStatus{
					Acquired: false,
					Error:    err,
				}
				return
			}

			//if Lock Error Channel is initialized, means all good and we acquired lock
			if rc.consul.LockErrorCh != nil {
				go rc.handleWaitForLockError()
				log.Println("[INFO] Lock acquired")
				rc.consul.LockIsWaiting = false
				rc.consul.LockIsHeld = true
				rc.consul.LockStatus <- &ConsulLockStatus{
					Acquired: true,
					Error:    nil,
				}
			}
		} else {
			log.Printf("[DEBUG] Lock is already held")
		}
	} else {
		log.Printf("[DEBUG] Already waiting for lock")
	}
}

func (rc *Resec) handleWaitForLockError() {
	log.Printf("[DEBUG] Starting Consul Lock Error Handler")

	rc.consul.LockWaitHandlerRunning = true
	rc.consul.LockStopWaiterHandlerCh = make(chan bool)

	select {
	case data, ok := <-rc.consul.LockErrorCh:
		if !ok {
			log.Printf("[DEBUG] Lock Error chanel is  closed")
			ErrMessage := fmt.Errorf("Consul lock lost or error")
			log.Printf("[DEBUG] %s", ErrMessage)
			rc.consul.LockIsWaiting = false
			rc.consul.LockIsHeld = false
			rc.consul.LockStatus <- &ConsulLockStatus{
				Acquired: false,
				Error:    ErrMessage,
			}
		} else {
			log.Printf("[DEBUG] something wrote to lock error channel %v ", data)
		}
	case <-rc.consul.LockStopWaiterHandlerCh:
		rc.consul.LockWaitHandlerRunning = false
		log.Printf("[DEBUG] Stopped Consul Lock Error handler")
	}
}

func (rc *Resec) AbortConsulLock() {

	if rc.consul.LockWaitHandlerRunning {
		log.Printf("[DEBUG] Stopping Consul Lock Error handler")
		close(rc.consul.LockStopWaiterHandlerCh)
	}
	if rc.consul.LockIsHeld {
		log.Println("[DEBUG] Lock is held, releasing")
		err := rc.consul.Lock.Unlock()
		if err != nil {
			log.Println("[ERROR] Can't release consul lock", err)
		} else {
			rc.consul.LockIsHeld = false
		}
	} else {
		if rc.consul.LockIsWaiting {
			log.Printf("[DEBUG] Stopping wait for consul lock")
			rc.consul.LockAbortCh <- struct{}{}
			rc.consul.LockIsWaiting = false
			log.Printf("[INFO] Stopped wait for consul lock")
		}
	}

}

func (rc *Resec) ServiceRegister(replicationRole string) error {

	nameToRegister := rc.consul.ServiceNamePrefix + "-" + replicationRole
	rc.consul.ServiceID = nameToRegister + ":" + rc.redis.Addr
	rc.consul.CheckID = rc.consul.ServiceID + ":replication-status-check"

	serviceInfo := &consulapi.AgentServiceRegistration{
		ID:   rc.consul.ServiceID,
		Port: rc.announcePort,
		Name: nameToRegister,
	}

	if rc.announceHost != "" {
		serviceInfo.Address = rc.announceHost
	}

	log.Printf("[DEBUG] Registering %s service in consul", serviceInfo.Name)

	err := rc.consul.Client.Agent().ServiceRegister(serviceInfo)
	if err != nil {
		rc.HandleConsulError(err)
		return err
	}

	log.Printf("[INFO] Registed service [%s](id [%s]) with address [%s:%d]", serviceInfo.Name, serviceInfo.ID, serviceInfo.Address, serviceInfo.Port)

	log.Printf("[DEBUG] Adding TTL Check with id %s to service %s with id %s", rc.consul.CheckID, nameToRegister, serviceInfo.ID)

	err = rc.consul.Client.Agent().CheckRegister(&consulapi.AgentCheckRegistration{
		Name:      replicationRole + " replication status",
		ID:        rc.consul.CheckID,
		ServiceID: rc.consul.ServiceID,
		AgentServiceCheck: consulapi.AgentServiceCheck{
			TTL:    rc.consul.TTL,
			Status: "critical",
			DeregisterCriticalServiceAfter: rc.consul.DeregisterServiceAfter.String(),
		},
	})

	if err != nil {
		log.Println("[ERROR] Consul Check registration failed", "error", err)
		rc.HandleConsulError(err)
		return err
	}

	log.Printf("[DEBUG] TTL Check added with id %s to service %s with id %s", rc.consul.CheckID, nameToRegister, serviceInfo.ID)

	return err
}

func (rc *Resec) SetConsulCheckStatus(output, status string) error {
	return rc.consul.Client.Agent().UpdateTTL(rc.consul.CheckID, output, status)
}

func (rc *Resec) WatchForMaster() error {
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

func (rc *Resec) HandleConsulError(err error) {

	if strings.Contains(err.Error(), "dial tcp") || strings.Contains(err.Error(), "Unexpected response code") {
		rc.consul.Healthy = false
		log.Printf("[ERROR] Consul Agent is down")
	}

}
