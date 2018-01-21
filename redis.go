package main

import (
	"log"
	"strconv"
	"time"
)

func (rc *Resec) RunAsSlave(masterAddress string, masterPort int) error {
	log.Printf("[DEBUG] Enslaving redis %s to be slave of %s:%d", rc.redis.Addr, masterAddress, masterPort)

	enslaveErr := rc.redis.Client.SlaveOf(masterAddress, strconv.Itoa(masterPort)).Err()

	if enslaveErr != nil {
		return enslaveErr
	}
	log.Printf("[INFO] Enslaved redis %s to be slave of %s:%d", rc.redis.Addr, masterAddress, masterPort)
	return nil
}

func (rc *Resec) RunAsMaster() error {
	promoteErr := rc.redis.Client.SlaveOf("no", "one").Err()

	if promoteErr != nil {
		return promoteErr
	} else {
		log.Println("[INFO] Promoted redis to Master")
	}

	return nil
}

func (rc *Resec) RedisHealthCheck() {

	for {

		log.Println("[DEBUG] Checking redis replication status")

		result, err := rc.redis.Client.Info("replication").Result()

		if err != nil {
			log.Printf("[ERROR] Can't connect to redis running on %s", rc.redis.Addr)
			rc.redisHealthCh <- &RedisHealth{
				Healthy: false,
			}
		} else {
			rc.redisHealthCh <- &RedisHealth{
				Output:  result,
				Healthy: true,
			}
		}

		time.Sleep(rc.healthCheckInterval)
	}

}
