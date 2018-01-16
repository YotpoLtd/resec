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

		if rh.Healthy != rc.lastRedisHealthCheckStatus {
			rc.lastRedisHealthCheckStatus = rh.Healthy

			if rh.Healthy {
				log.Printf("[INFO] Redis HealthCheck changed to healthy")
				rc.watchForMaster()
				go rc.waitForLock()
				go rc.runAsMaster()
				go rc.runAsSlave()

			} else {
				log.Printf("[INFO] Redis HealthCheck changed to NOT healthy")
				rc.AbortConsulLock()
				rc.stopWatchForMaster()
			}

		}

	}
}

func (rc *resecConfig) Stop() {
	log.Printf("[DEBUG] Stopping redis monitor")
	rc.redisMonitorEnabled = false
	log.Printf("[DEBUG] Force fail redis health status")
	rc.lastRedisHealthCheckStatus = false
	rc.stopWatchForMaster()
	rc.AbortConsulLock()

	if rc.consulServiceId != "" {
		log.Printf("[INFO] Deregisted service (id [%s])", rc.consulServiceId)
		err := rc.consulClient.Agent().ServiceDeregister(rc.consulServiceId)
		if err != nil {
			log.Printf("[ERROR] Can't deregister consul service, %s", err)
		}
	}
	log.Printf("[INFO] Finish!")
}
