package main

import (
	"log"
)

func (rc *resecConfig) Run() {
	for {

		rh := <-rc.redisHealthCh

		// update Consul Helthcheck
		if rc.consulCheckId != "" {
			var err error
			if rh.Healthy {
				log.Printf("[DEBUG] Redis health OK, sending update to Consul")
				err = rc.consulClient.Agent().UpdateTTL(rc.consulCheckId, rh.Output, "pass")
				if err != nil {
					log.Printf("[ERROR] Failed to update consul Check TTL - %s", err)
				}
			} else {
				log.Printf("[DEBUG] Redis health NOT OK, sending update to Consul")
				err = rc.consulClient.Agent().UpdateTTL(rc.consulCheckId, "", "fail")
				if err != nil {
					log.Printf("[ERROR] Failed to update consul Check TTL - %s", err)
				}
				rc.consulCheckId = ""
			}
		}

		if rh.Healthy != rc.lastRedisHealthCheckOK {
			rc.lastRedisHealthCheckOK = rh.Healthy

			if rh.Healthy {
				log.Printf("[DEBUG] Redis HealthCheck changed to healthy")
				// run the master service watcher
				rc.Watch()
				go rc.waitForLock()
				go rc.runAsMaster()
				go rc.runAsSlave()

			} else {
				log.Printf("[DEBUG] Redis HealthCheck changed to NOT healthy")
				// disable Waiting for consul lock
				rc.AbortConsulLock()
				//Stopping the watch for master
				rc.stopWatchCh <- struct{}{}

			}

		}

	}
}

func (rc *resecConfig) Stop() {
	rc.redisMonitorEnabled = false
	rc.lastRedisHealthCheckOK = false
	if rc.masterWatchRunning {
		//Stopping the watch for master
		rc.stopWatchCh <- struct{}{}
	}
	rc.AbortConsulLock()

	if rc.consulServiceId != "" {
		err := rc.consulClient.Agent().ServiceDeregister(rc.consulServiceId)
		if err != nil {
			log.Printf("[ERROR] Can't deregister consul service, %s", err)
		}
	}
}
