package main

import (
	"github.com/go-redis/redis"
	"log"
	"strconv"
	"time"
)

func (rc *resecConfig) RedisClientInit() {

	redisOptions := &redis.Options{
		Addr:        rc.redis.Addr,
		DialTimeout: rc.healthCheckTimeout,
		ReadTimeout: rc.healthCheckTimeout,
	}

	if rc.redis.Password != "" {
		redisOptions.Password = rc.redis.Password
	}

	rc.redis.Client = redis.NewClient(redisOptions)

}

func (rc *resecConfig) RunAsSlave(masterAddress string, masterPort int) {
	log.Printf("[DEBUG] Enslaving redis %s to be slave of %s:%d", rc.redis.Addr, masterAddress, masterPort)

	enslaveErr := rc.redis.Client.SlaveOf(masterAddress, strconv.Itoa(masterPort)).Err()

	if enslaveErr != nil {
		log.Printf("[ERROR] Failed to enslave redis to %s:%d - %s", masterAddress, masterPort, enslaveErr)
	}
	log.Printf("[INFO] Enslaved redis %s to be slave of %s:%d", rc.redis.Addr, masterAddress, masterPort)

	rc.redis.ReplicationStatus = "slave"
}

func (rc *resecConfig) RunAsMaster() {
	promoteErr := rc.redis.Client.SlaveOf("no", "one").Err()

	if promoteErr != nil {
		log.Printf("[ERROR] Failed to promote  redis to master - %s", promoteErr)
	} else {
		rc.redis.ReplicationStatus = "master"
		log.Println("[INFO] Promoted redis to Master")
	}

}

func (rc *resecConfig) RedisHealthCheck() {

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
