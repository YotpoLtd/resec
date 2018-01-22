package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	consulapi "github.com/hashicorp/consul/api"
)

func (rc *Resec) Start() {

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)

	for {
		select {
		case <-c:
			log.Printf("[INFO] Caught signal, releasing lock and stopping...")
			return
		case health := <-rc.redisHealthCh:
			log.Printf("[DEBUG] Read from redis health channel")

			if rc.consul.Healthy {

				// update Consul HealthCheck
				if rc.consul.CheckID != "" {
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
						rc.HandleConsulError(err)
						log.Printf("[ERROR] Failed to update consul Check TTL - %s", err)
						if rc.consul.Healthy {
							rc.ServiceRegister(rc.redis.ReplicationStatus)
						}
					}
				}
			} else {
				log.Printf("[DEBUG] Consul is not healthy, skipping service check update")
			}
			// handle status change
			if health.Healthy != rc.redis.Healthy {
				rc.redis.Healthy = health.Healthy
				if health.Healthy {
					log.Printf("[INFO] Redis HealthCheck changed to healthy")
					if rc.redis.ReplicationStatus == "slave" {
						err := rc.RunAsSlave(rc.lastKnownMasterInfo.Address, rc.lastKnownMasterInfo.Port)
						if err != nil {
							continue
						}
					}
					go rc.TryWaitForLock()
				} else {
					log.Printf("[INFO] Redis HealthCheck changed to NOT healthy")
					rc.AbortConsulLock()
				}
			}

		case masterConsulServiceInfo := <-rc.masterConsulServiceCh:
			log.Printf("[DEBUG] Read from master channel")
			rc.consul.Healthy = true
			masterCount := len(masterConsulServiceInfo)
			switch {
			case masterCount > 1:
				log.Printf("[ERROR] Found more than one master registered in Consul")
				continue
			case masterCount == 0:
				log.Printf("[INFO] No redis master services in Consul")
				if rc.redis.Healthy {
					go rc.TryWaitForLock()

				} else {
					log.Printf("[DEBUG] Redis is not healthy, nothing to do here")
				}
			default:
				currentMaster := masterConsulServiceInfo[0]

				currentMasterInfo := rc.ParseMasterInfo(currentMaster)

				if currentMasterInfo != rc.lastKnownMasterInfo {
					log.Printf("[INFO] Redis master updated in Consul")
					rc.lastKnownMasterInfo = currentMasterInfo
					if currentMaster.Service.ID == rc.consul.ServiceID {
						log.Printf("[DEBUG] Current master is my redis, nothing to do")
						continue
					}

					enslaveErr := rc.RunAsSlave(rc.lastKnownMasterInfo.Address, rc.lastKnownMasterInfo.Port)
					if enslaveErr != nil {
						log.Printf("[ERROR] Failed to enslave redis to %s:%d - %s", rc.lastKnownMasterInfo.Address, rc.lastKnownMasterInfo.Port, enslaveErr)
					}
					rc.redis.ReplicationStatus = "slave"
					registerErr := rc.ServiceRegister(rc.redis.ReplicationStatus)
					if registerErr != nil {
						log.Printf("[ERROR] Consul Service registration failed - %s", registerErr)
					}
					// if non of the above didn't failed
					if enslaveErr == nil && registerErr == nil {
						go rc.TryWaitForLock()
					}
				}
			}
		case lockStatus := <-rc.consul.LockStatus:
			log.Printf("[DEBUG] Read from lock channel")

			if lockStatus.Acquired {
				// deregister slave before promoting to master
				if rc.redis.ReplicationStatus == "slave" {
					err := rc.consul.Client.Agent().ServiceDeregister(rc.consul.ServiceID)
					if err != nil {
						rc.HandleConsulError(err)
						log.Printf("[ERROR] Can't deregister consul service, %s", err)
					}
				}
				if rc.redis.Healthy {
					err := rc.RunAsMaster()
					if err != nil {
						log.Printf("[ERROR] Failed to promote  redis to master - %s", err)
						rc.AbortConsulLock()
						continue
					}
					rc.redis.ReplicationStatus = "master"
					rc.ServiceRegister(rc.redis.ReplicationStatus)
				}
			}
			if lockStatus.Error != nil {
				log.Printf("[ERROR] %s", lockStatus.Error)
				rc.HandleConsulError(lockStatus.Error)
				if rc.consul.Healthy {
					if rc.redis.ReplicationStatus == "master" {
						// Failing master check in consul
						consulCheckUpdateErr := rc.SetConsulCheckStatus("Lock lost or error", "fail")
						rc.HandleConsulError(consulCheckUpdateErr)
						// invalidating CheckID to avoid redis healthcheck to update master service
						rc.consul.CheckID = ""
						rc.redis.ReplicationStatus = ""
					}
					go rc.TryWaitForLock()
				}
			}
		}
	}
}

func (rc *Resec) ParseMasterInfo(consulServiceInfo *consulapi.ServiceEntry) RedisInfo {

	var redisInfo RedisInfo
	// Use master node address if it's registered without service address
	if consulServiceInfo.Service.Address != "" {
		redisInfo.Address = consulServiceInfo.Service.Address
	} else {
		redisInfo.Address = consulServiceInfo.Node.Address
	}
	redisInfo.Port = consulServiceInfo.Service.Port

	return redisInfo

}

func (rc *Resec) Stop() {
	rc.AbortConsulLock()

	if rc.consul.ServiceID != "" {
		log.Printf("[INFO] Deregisted service (id [%s])", rc.consul.ServiceID)
		err := rc.consul.Client.Agent().ServiceDeregister(rc.consul.ServiceID)
		if err != nil {
			log.Printf("[ERROR] Can't deregister consul service, %s", err)
		}
	}
	log.Printf("[INFO] Finish!")
}
