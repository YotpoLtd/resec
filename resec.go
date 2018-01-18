package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func (rc *resecConfig) Start() {

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)

	for {
		select {
		case <-c:
			log.Printf("[INFO] Caught signal, releasing lock and stopping...")
			return
		case health := <-rc.redisHealthCh:

			// update Consul HealthCheck
			if rc.consul.CheckId != "" {
				var err error
				var status string
				if health.Healthy {
					log.Printf("[DEBUG] Redis health OK, sending update to Consul")
					status = "pass"
				} else {
					log.Printf("[DEBUG] Redis health NOT OK, sending update to Consul")
					status = "fail"
				}
				err = rc.SetConsulCheckStatus(health.Output, status)
				if err != nil {
					log.Printf("[ERROR] Failed to update consul Check TTL - %s", err)
					rc.ServiceRegister(rc.redis.ReplicationStatus)
				}
			}
			// handle status change
			if health.Healthy != rc.redis.Healthy {
				rc.redis.Healthy = health.Healthy
				if health.Healthy {
					log.Printf("[INFO] Redis HealthCheck changed to healthy")
					if rc.redis.ReplicationStatus == "slave" {
						rc.RunAsSlave(rc.lastKnownMasterAddress, rc.lastKnownMasterPort)
					}
					if !rc.consul.LockIsWaiting {
						if !rc.consul.LockIsHeld {
							go rc.WaitForLock()
						}
					}
				} else {
					log.Printf("[INFO] Redis HealthCheck changed to NOT healthy")
					rc.AbortConsulLock()
				}
			}

		case masterConsulServiceInfo := <-rc.masterConsulServiceCh:
			masterCount := len(masterConsulServiceInfo)
			switch {
			case masterCount > 1:
				log.Printf("[ERROR] Found more than one master registered in Consul")
				continue
			case masterCount == 0:
				log.Printf("[INFO] No redis master services in Consul")
				if rc.redis.Healthy {
					if !rc.consul.LockIsWaiting {
						if !rc.consul.LockIsHeld {
							go rc.WaitForLock()
						}
					}
				} else {
					log.Printf("[DEBUG] Redis is not healthy, nothing to do here")
				}
			default:
				log.Printf("[INFO] Redis master updated in Consul")
				currentMaster := masterConsulServiceInfo[0]
				if currentMaster.Service.ID == rc.consul.ServiceId {
					log.Printf("[DEBUG] Current master is my redis, nothing to do")
					continue
				}

				// Use master node address if it's registered without service address
				if currentMaster.Service.Address != "" {
					rc.lastKnownMasterAddress = currentMaster.Service.Address
				} else {
					rc.lastKnownMasterAddress = currentMaster.Node.Address
				}
				rc.lastKnownMasterPort = currentMaster.Service.Port
				rc.RunAsSlave(rc.lastKnownMasterAddress, rc.lastKnownMasterPort)
				rc.ServiceRegister(rc.redis.ReplicationStatus)
				if !rc.consul.LockIsWaiting {
					if !rc.consul.LockIsHeld {
						go rc.WaitForLock()
					}
				}
			}
		case lockStatus := <-rc.consul.LockStatus:
			if lockStatus.Acquired {
				rc.RunAsMaster()
				rc.ServiceRegister(rc.redis.ReplicationStatus)
			}
			if lockStatus.Error != nil {
				log.Printf("[ERROR] %s", lockStatus.Error)
				rc.WaitForLock()
			}

		}
	}
}

func (rc *resecConfig) Stop() {
	rc.AbortConsulLock()

	if rc.consul.ServiceId != "" {
		log.Printf("[INFO] Deregisted service (id [%s])", rc.consul.ServiceId)
		err := rc.consul.Client.Agent().ServiceDeregister(rc.consul.ServiceId)
		if err != nil {
			log.Printf("[ERROR] Can't deregister consul service, %s", err)
		}
	}
	log.Printf("[INFO] Finish!")
}
