package main

import (
	log "github.com/Sirupsen/logrus"
	consulapi "github.com/hashicorp/consul/api"
	consulwatch "github.com/hashicorp/consul/watch"
)

// Wait for lock to the Consul KV key.
// This will ensure we are the only master is holding a lock and registered
func (rc *resecConfig) WaitForLock() {
	log.Info("Trying to acquire leader lock")
	consulClient := rc.consulClient()

	lock, err := consulClient.LockOpts(&consulapi.LockOptions{
		Key:         rc.consulLockKey,
		SessionName: "ReSeC Lock",
		SessionTTL:  "10s",
	})

	if err != nil {
		rc.errCh <- err
	}

	rc.lockCh, err = lock.Lock(rc.lockAbortCh)

	if err != nil {
		rc.errCh <- err
	}

	log.Info("Lock acquired")
	rc.RunAsMaster()
}

func (rc *resecConfig) ServiceRegister(replication_role string) error {
	serviceInfo := &consulapi.AgentServiceRegistration{
		Port: rc.announcePort,
		Name: rc.consulServiceName + "-" + replication_role,
		Check: &consulapi.AgentServiceCheck{
			TCP:      rc.redisAddr,
			Interval: rc.consulCheckInterval,
			Timeout:  rc.consulCheckTimeout,
		},
	}

	if rc.announceHost != "" {
		serviceInfo.Address = rc.announceHost
	}

	err := rc.consulClient().Agent().ServiceRegister(serviceInfo)
	if err != nil {
		log.Error("consul Service registration failed", "error", err)
		return err
	}
	log.Info("registration with consul completed", "sinfo", serviceInfo)
	return err
}

func (rc *resecConfig) Watch() error {
	params := map[string]interface{}{
		"type":        "service",
		"service":     rc.consulServiceName + "-master",
		"passingonly": true,
	}

	wp, err := consulwatch.Parse(params)
	if err != nil {
		log.Error("couldn't create a watch plan", "error", err)
		return err
	}

	wp.Handler = func(idx uint64, data interface{}) {
		switch srvcs := data.(type) {
		case []*consulapi.ServiceEntry:
			log.Debug("got an array of ServiceEntry", "srvcs", srvcs)
			rc.masterCh <- srvcs
		default:
			log.Debug("got an unknown interface", "srvcs", srvcs)
		}

	}
	go func() {
		if err := wp.Run(rc.consulClientConfig.Address); err != nil {
			log.Error("got an error watching for changes", "error", err)
		}
	}()

	//Check if we should quit
	//wait forever for a stop signal to happen
	go func() {
		select {
		case <-rc.stopWatchCh:
			log.Debug("Stopped the watch, cause i'm the master")
			wp.Stop()
		default:
		}
	}()

	return nil
}
