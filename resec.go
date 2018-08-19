package main

import (
	"log"

	consulapi "github.com/hashicorp/consul/api"
)

//start starts the procedure
func (rc *resec) run() {
	defer func() { rc.cleanup() }()

	for {
		select {
		// got signal from the OS
		case <-rc.sigCh:
			log.Printf("[INFO] Caught signal, stopping worker loop")
			return

		// got internal request to shut down
		case <-rc.stopCh:
			log.Printf("[INFO] Shutdown requested, stopping worker loop")
			return

		// got an update on redis replication status
		case update, ok := <-rc.redisReplicationCh:
			if !ok {
				log.Println("[ERROR] Redis replication channel was closed, shutting down")
				return
			}

			log.Printf("[DEBUG] Got redis replication info update")

			if rc.consul.healthy {
				// if we don't have any check id, we haven't registered our service yet
				// let's do that first
				if rc.redis.replicationStatus != "" {
					if rc.consul.checkID == "" {
						log.Printf("[DEBUG] Consul Check ID is not generated")
						rc.registerService()
					}

					var status string
					if update.healthy {
						log.Printf("[DEBUG] Redis health OK, sending update to Consul")
						status = "pass"
					} else {
						log.Printf("[DEBUG] Redis health NOT OK, sending update to Consul")
						status = "fail"
					}

					if err := rc.setConsulCheckStatus(update.output, status); err != nil {
						rc.handleConsulError(err)
						log.Printf("[ERROR] Failed to update consul Check TTL - %s", err)
					}
				} else {
					log.Printf("[DEBUG] Redis replication status is not defined")
				}
			} else {
				log.Printf("[INFO] Consul is not healthy, skipping service check update")
			}

			// no change in health
			if update.healthy == rc.redis.healthy {
				continue
			}

			rc.redis.healthy = update.healthy

			// our state is now unhealthy, release the consul lock so someone else can
			// acquire the consul leadership and become redis master
			if !update.healthy {
				log.Printf("[INFO] Redis status changed to NOT healthy")
				rc.releaseConsulLock()
				continue
			}

			log.Printf("[INFO] Redis status changed to healthy")
			if rc.redis.replicationStatus != "master" {
				if err := rc.runAsSlave(rc.lastKnownMasterInfo.address, rc.lastKnownMasterInfo.port); err != nil {
					log.Println(err)
					continue
				}
			}

		case update, ok := <-rc.consulMasterServiceCh:
			if !ok {
				log.Printf("[ERROR] Consul master service channel was closed, shutting down")
				return
			}

			log.Printf("[DEBUG] Got consul master service status update")
			rc.consul.healthy = true
			masterCount := len(update)

			switch {
			// no master means we can attempt to acquire leadership
			case masterCount == 0:
				log.Printf("[INFO] No redis master services in Consul")
				if rc.redis.healthy {
					go rc.acquireConsulLeadership()
					continue
				}
				log.Printf("[DEBUG] No Master found in consul, but redis is not healthy, nothing to do here")

			// multiple masters is not good
			case masterCount > 1:
				log.Printf("[ERROR] Found more than one master registered in Consul")
				continue

			// a single master was found
			case masterCount == 1:
				currentMaster := update[0]
				currentMasterInfo := rc.parseMasterInfo(currentMaster)

				// no change in master data, nothing for us to do here
				if rc.lastKnownMasterInfo == currentMasterInfo {
					continue
				}

				rc.lastKnownMasterInfo = currentMasterInfo

				log.Printf("[INFO] Redis master updated in Consul")
				if currentMaster.Service.ID == rc.consul.serviceID {
					log.Printf("[DEBUG] Current master is my redis, nothing to do")
					continue
				}

				// todo(jippi): if we can't enslave our redis, we shouldn't try to do any further work
				//              especially not updating our consul catalog entry
				if !rc.redis.healthy {
					continue
				}
				if err := rc.runAsSlave(rc.lastKnownMasterInfo.address, rc.lastKnownMasterInfo.port); err != nil {
					log.Println(err)
					continue
				}
			}

		// if our consul lock status has changed
		case update := <-rc.consul.lockStatusCh:
			log.Printf("[DEBUG] Read from lock channel")

			if update.acquired {
				// deregister slave before promoting to master
				if rc.redis.replicationStatus == "slave" {
					if err := rc.consul.client.Agent().ServiceDeregister(rc.consul.serviceID); err != nil {
						rc.handleConsulError(err)
						log.Printf("[ERROR] Can't deregister consul service, %s", err)
						// todo(jippi): if we can't deregister our self, this can get super messy, should we exit here?
					}
				}

				if rc.redis.healthy {
					if err := rc.runAsMaster(); err != nil {
						log.Printf("[ERROR] Failed to promote redis to master - %s", err)
						rc.releaseConsulLock()
						continue
					}

					rc.redis.replicationStatus = "master"
					rc.registerService()
				}
			}

			if update.err != nil {
				log.Printf("[ERROR] %s", update.err)
				rc.handleConsulError(update.err)

				if !rc.consul.healthy {
					continue
				}

				if rc.redis.replicationStatus == "master" {
					// Failing master check in consul
					if err := rc.setConsulCheckStatus("Lock lost or error", "fail"); err != nil {
						rc.handleConsulError(err)
					}

					// invalidating CheckID to avoid redis healthcheck to update master service
					rc.consul.checkID = ""
					rc.redis.replicationStatus = ""
				}

				go rc.acquireConsulLeadership()
			}
		}
	}
}

// parseMasterInfo parses consulServiceInfo
func (rc *resec) parseMasterInfo(consulServiceInfo *consulapi.ServiceEntry) redisInfo {
	info := redisInfo{
		address: consulServiceInfo.Node.Address,
		port:    consulServiceInfo.Service.Port,
	}

	// Use master node address if it's registered without service address
	if consulServiceInfo.Service.Address != "" {
		info.address = consulServiceInfo.Service.Address
	}

	return info
}

// cleanup will clenup locks and similar internal state
func (rc *resec) cleanup() {
	rc.releaseConsulLock()

	if rc.consul.serviceID != "" {
		log.Printf("[INFO] Deregisted service (id [%s])", rc.consul.serviceID)
		if err := rc.consul.client.Agent().ServiceDeregister(rc.consul.serviceID); err != nil {
			log.Printf("[ERROR] Can't deregister consul service, %s", err)
		}
	}

	log.Printf("[INFO] Finish!")
}
