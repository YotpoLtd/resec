package main

import (
	"fmt"
	log "github.com/Sirupsen/logrus"
	"github.com/go-redis/redis"
	consulapi "github.com/hashicorp/consul/api"
	"strconv"
	"strings"
)


func RunAsMaster(consulClient *consulapi.Client, redisClient *redis.Client, resecConfig *ResecConfig) {

	log.Infof("Ok, My time to be the master, let's go!")

	lock, sessionID, err := WaitForLock(consulClient, resecConfig.consulLockKey)

	if err != nil {
		fmt.Println(err)
	}

	fmt.Println(sessionID, err)

	ServiceRegister(consulClient, resecConfig, "master",  "5s", "2s")

	consulClient.Session().RenewPeriodic("10s", sessionID, nil, nil)

	defer lock.Unlock()

	fmt.Println("I'm done")

}

func RunAsSlave(consulClient *consulapi.Client, redisClient *redis.Client, resecConfig *ResecConfig, currentMaster *consulapi.ServiceEntry) {
	log.Infof("Ok, There's a healthy master in consul, I'm obeying this!")
	redisClient.SlaveOf(currentMaster.Service.Address, strconv.Itoa(currentMaster.Service.Port))

}

func PromoteSlave(c *consulapi.Client, redisclient *redis.Client, rc *ResecConfig) {
	log.Infof("Master is dead, let's Rock'n'Roll!")
	redisclient.SlaveOf("no", "one")
}

func (s *ResecConfig) Start() error {
	// Stop chan for all tasks to depend on
	s.stopCh = make(chan interface{})

	go s.run()

	return nil
}

// Stop ...
func (s *ResecConfig) Stop() {
	close(s.stopCh)
}

func (*ResecConfig) run() {

}


func main() {

	runConf := DefaultConfig()
	consulClient, err := consulapi.NewClient(runConf.consulClientConfig)

	log.Debugf("heres the client", consulClient)

	if err != nil {
		log.Fatalf("Can't connect to Consul on %s", runConf.consulClientConfig.Address)
	}

	redisClient := redis.NewClient(&redis.Options{
		Addr: runConf.redisAddr,
	})

	_, err = redisClient.Ping().Result()

	if err != nil {
		log.Fatalf("Can't Connect to redis running on %s", runConf.redisAddr)
	}

	var replicationRole string
	replicationInfo := redisClient.Info("replication").String()

	line := strings.Split(strings.TrimSuffix(replicationInfo, "\n"), "\n")[1]
	if strings.HasPrefix(line, "role:") {
		replicationRole = strings.TrimSuffix(strings.TrimPrefix(line, "role:"), "\r")
	} else {
		log.Fatalf("Redis returned [%s] instead of replication role", line)
	}

	if replicationRole == "master" {
		log.Infof("Redis is %s, checking the service in consul", replicationRole)
	}

	Watch(runConf)

	//consulServiceHealth, _, err := consulClient.Health().Service(runConf.consulServiceName, "master", true, nil)
	//
	//if err != nil {
	//	log.Errorf("Can't check consul service, %s", err)
	//}
	//
	//consulServiceHealthLenght := len(consulServiceHealth)
	//
	//switch {
	//case consulServiceHealthLenght > 1:
	//	log.Fatalf("There is more than one Master registered in Consul")
	//case consulServiceHealthLenght == 0:
	//	log.Infof("Consul Service is not registered let's do the magic!")
	//	RunAsMaster(consulClient, redisClient, runConf)
	//default:
	//	RunAsSlave(consulClient, redisClient, runConf, consulServiceHealth[0])
	//}
	//
	//log.Debugf("Consul Service status is %s", consulServiceHealth)
}
