package main

import (
	"fmt"

	"github.com/go-redis/redis"
	consul "github.com/hashicorp/consul/api"

	log "github.com/Sirupsen/logrus"
	"os"
	"strconv"
)

const (
	consulLockKey   = "reclaim/allocations.lock"
	consulLockValue = "reclaim/allocations.value"
)

// Wait for lock to the Consul KV key.
// This will ensure we are the only master is holding a lock and registered
func WaitForLock(key string) (*consul.Lock, string, error) {
	client, err := consul.NewClient(consul.DefaultConfig())
	if err != nil {
		return nil, "", err
	}

	log.Info("Trying to acquire leader lock")
	sessionID, err := session(client)
	if err != nil {
		return nil, "", err
	}

	lock, err := client.LockOpts(&consul.LockOptions{
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
func session(c *consul.Client) (string, error) {
	s := c.Session()
	se := &consul.SessionEntry{
		Name: "RECLaiM",
		TTL:  "10s",
	}

	id, _, err := s.Create(se, nil)
	if err != nil {
		return "", err
	}

	return id, nil
}

func ServiceRegister(name string, address string, port int, interval string, timeout string) error {
	client, err := consul.NewClient(consul.DefaultConfig())
	if err != nil {
		log.Error("failed to connect to consul")
	}

	sinfo := &consul.AgentServiceRegistration{
		Tags:    []string{"master"},
		Port:    port,
		Address: address,
		Name:    name,
		Check: &consul.AgentServiceCheck{
			TCP:      address + ":" + strconv.Itoa(port),
			Interval: interval,
			Timeout:  timeout,
		},
	}
	err = client.Agent().ServiceRegister(sinfo)
	if err != nil {
		log.Error("consul Service registration failed", "error", err)
		return err
	}
	log.Info("registration with consul completed", "sinfo", sinfo)
	return err
}

func main() {

	consul_service_name := os.Getenv("CONSUL_SERVICE_NAME")
	redis_host := os.Getenv("REDIS_HOST")
	redis_port := os.Getenv("REDIS_PORT")
	redis_port_int, err := strconv.Atoi(redis_port)
	if err != nil {
		fmt.Println("Cannot convert to int REDIS_PORT, %s", err)
	}
	lock, sessionID, err := WaitForLock(consulLockKey)

	if err != nil {
		fmt.Println(err)
	}

	fmt.Println(sessionID, err)

	cclient, err := consul.NewClient(consul.DefaultConfig())

	redisclient := redis.NewClient(&redis.Options{
		Addr: redis_host + ":" + redis_port,
	})

	redisclient.SlaveOf("no", "one")

	ServiceRegister(consul_service_name, redis_host, redis_port_int, "5s", "2s")

	cclient.Session().RenewPeriodic("10s", sessionID, nil, nil)

	defer lock.Unlock()

	fmt.Println("I'm done")


}
