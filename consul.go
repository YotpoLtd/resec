package main

import (
	log "github.com/Sirupsen/logrus"
	consulapi "github.com/hashicorp/consul/api"
	consulwatch "github.com/hashicorp/consul/watch"
)


// Wait for lock to the Consul KV key.
// This will ensure we are the only master is holding a lock and registered
func WaitForLock(c *consulapi.Client, key string) (*consulapi.Lock, string, error) {
	log.Info("Trying to acquire leader lock")
	sessionID, err := session(c)
	if err != nil {
		return nil, "", err
	}

	lock, err := c.LockOpts(&consulapi.LockOptions{
		Key:     key,
		Session: sessionID,
	})
	if err != nil {
		return nil, "", err
	}

	_, err = lock.Lock(nil)
	if err != nil {
		return nil, "", err
	}

	log.Info("Lock acquired")
	return lock, sessionID, nil
}

// Create a Consul session used for locks
func session(c *consulapi.Client) (string, error) {
	s := c.Session()
	se := &consulapi.SessionEntry{
		Name: "ReSeC",
		TTL:  "10s",
	}

	id, _, err := s.Create(se, nil)
	if err != nil {
		return "", err
	}

	return id, nil
}

func ServiceRegister(c *consulapi.Client, resecConfig *ResecConfig, replication_role string, interval string, timeout string) error {
	serviceInfo := &consulapi.AgentServiceRegistration{
		Tags:    []string{replication_role},
		Port:    resecConfig.redisPort,
		Address: resecConfig.redisHost,
		Name:    resecConfig.consulServiceName,
		Check: &consulapi.AgentServiceCheck{
			TCP:      resecConfig.redisAddr,
			Interval: interval,
			Timeout:  timeout,
		},
	}

	err := c.Agent().ServiceRegister(serviceInfo)
	if err != nil {
		log.Error("consul Service registration failed", "error", err)
		return err
	}
	log.Info("registration with consul completed", "sinfo", serviceInfo)
	return err
}



func  Watch(resecConfig *ResecConfig) (err error) {
	params := map[string]interface{}{
		"type":        "service",
		"service":     resecConfig.consulServiceName,
		"tag":         "master",
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
		default:
			log.Debug("got an unknown interface", "srvcs", srvcs)
		}

	}
	go func() {
		if err := wp.Run(resecConfig.consulClientConfig.Address); err != nil {
			log.Error("got an error watching for changes", "error", err)
		}
	}()
	return
}



