package main

import (
	"fmt"
	"log"
	"strconv"
	"time"
)

// runAsSlave sets the instance to be a slave for the master
func (rc *resec) runAsSlave(masterAddress string, masterPort int) error {
	log.Printf("[DEBUG] Enslaving redis %s to be slave of %s:%d", rc.redis.address, masterAddress, masterPort)

	if err := rc.redis.client.SlaveOf(masterAddress, strconv.Itoa(masterPort)).Err(); err != nil {
		return fmt.Errorf("[ERROR] Could not enslave redis %s to be slave of %s:%d (%v)", rc.redis.address, masterAddress, masterPort, err)
	}

	log.Printf("[INFO] Enslaved redis %s to be slave of %s:%d", rc.redis.address, masterAddress, masterPort)
	return nil
}

// runAsMaster sets the instance to be the master
func (rc *resec) runAsMaster() error {
	if err := rc.redis.client.SlaveOf("no", "one").Err(); err != nil {
		return err
	}

	log.Println("[INFO] Promoted redis to Master")
	return nil
}

// watchRedisReplicationStatus checks redis replication status
func (rc *resec) watchRedisReplicationStatus() {
	timer := time.NewTimer(rc.healthCheckInterval)

	for {
		select {
		case <-timer.C:
			log.Println("[DEBUG] Checking redis replication status")

			result, err := rc.redis.client.Info("replication").Result()
			if err != nil {
				log.Printf("[ERROR] Can't connect to redis running on %s", rc.redis.address)
				rc.redisReplicationCh <- &redisHealth{
					output:  fmt.Sprintf("Can't connect to redis running on %s", rc.redis.address),
					healthy: false,
				}

				timer.Reset(rc.healthCheckInterval)
				continue
			}

			rc.redisReplicationCh <- &redisHealth{
				output:  result,
				healthy: true,
			}

			timer.Reset(rc.healthCheckInterval)
		}
	}
}
