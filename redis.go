package main

import (
	"log"
	"strconv"
	"time"

	"github.com/go-redis/redis"
)

func (rc *resecConfig) redisClientInit() {

	redisOptions := &redis.Options{
		Addr:        rc.redisAddr,
		DialTimeout: rc.healthCheckTimeout,
		ReadTimeout: rc.healthCheckTimeout,
	}

	if rc.redisPassword != "" {
		redisOptions.Password = rc.redisPassword
	}

	rc.redisClient = redis.NewClient(redisOptions)
}

func (rc *resecConfig) runAsMaster() {
	for {
		if promote := <-rc.promoteCh; promote {
			rc.stopWatchForMaster()
			rc.promote()
		}
	}

}

func (rc *resecConfig) runAsSlave() {

	for {
		currentMaster := <-rc.masterConsulServiceCh

		// Use master node address if it's registered without service address
		var masterAddress string
		if currentMaster.Service.Address != "" {
			masterAddress = currentMaster.Service.Address
		} else {
			masterAddress = currentMaster.Node.Address
		}

		log.Printf("[INFO] Enslaving redis %s to be slave of %s:%d", rc.redisAddr, masterAddress, currentMaster.Service.Port)

		enslaveErr := rc.redisClient.SlaveOf(masterAddress, strconv.Itoa(currentMaster.Service.Port)).Err()

		if enslaveErr != nil {
			log.Printf("[ERROR] Failed to enslave redis to %s:%d - %s", masterAddress, currentMaster.Service.Port, enslaveErr)
		}

		rc.serviceRegister("slave")
	}
}

func (rc *resecConfig) promote() {
	promoteErr := rc.redisClient.SlaveOf("no", "one").Err()

	if promoteErr != nil {
		log.Printf("[ERROR] Failed to promote  redis to master - %s", promoteErr)
	} else {
		rc.serviceRegister("master")
		log.Println("[INFO] Promoted redis to Master")
	}

}

func (rc *resecConfig) redisHealthCheck() {

	for rc.redisMonitorEnabled {

		log.Print("[DEBUG] Checking redis replication status")

		result, err := rc.redisClient.Info("replication").Result()

		if err != nil {
			log.Printf("[ERROR] Can't connect to redis running on %s", rc.redisAddr)
			rc.redisHealthCh <- &redisHealth{
				Output:  "",
				Healthy: false,
			}
		} else {
			rc.redisHealthCh <- &redisHealth{
				Output:  result,
				Healthy: true,
			}
		}

		time.Sleep(rc.healthCheckInterval)
	}

}
