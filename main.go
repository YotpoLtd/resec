package main

import (
	log "github.com/Sirupsen/logrus"
	consulapi "github.com/hashicorp/consul/api"
	"strconv"
)

func (rc *resecConfig) RunAsMaster() {

	log.Infof("Ok, My time to be the master, let's go!")

	close(rc.stopWatchCh)

	rc.Promote()

	rc.ServiceRegister("master")

	rc.consulClient().Session().RenewPeriodic("10s", rc.sessionId, &consulapi.WriteOptions{Token: rc.consulClientConfig.Token}, rc.stopCh)

}

func (rc *resecConfig) RunAsSlave(currentMaster *consulapi.ServiceEntry) {
	log.Infof("Ok, There's a healthy master in consul, I'm obeying this!")
	rc.redisClient().SlaveOf(currentMaster.Service.Address, strconv.Itoa(currentMaster.Service.Port))
	rc.ServiceRegister("slave")
}

func (rc *resecConfig) Promote() {
	log.Infof("Promoting redis to Master, let's Rock'n'Roll!")
	rc.redisClient().SlaveOf("no", "one")
}

//
//func (r *resecConfig) Start() error {
//	// Stop chan for all tasks to depend on
//	r.stopCh = make(chan interface{})
//
//	go r.run()
//
//	return nil
//}
//
//// Stop ...
//func (s *resecConfig) Stop() {
//	close(s.stopCh)
//}
//
//func (*resecConfig) run() {
//
//}

//// Close the stopCh if we get a signal, so we can gracefully shut down
//func (r *resecConfig) signalHandler() {
//	c := make(chan os.Signal, 1)
//	signal.Notify(c, os.Interrupt)
//
//	select {
//	case <-c:
//		log.Info("Caught signal, releasing lock and stopping...")
//		r.Stop()
//	case <-r.stopCh:
//		break
//	}
//}

func main() {

	// init the config
	runConf := defaultConfig()

	// start waiting for lock
	go runConf.WaitForLock()

	// run the master service watcher
	runConf.Watch()

	for m := range runConf.masterCh {
		consulServiceHealthLength := len(m)

		switch {
		case consulServiceHealthLength > 1:
			log.Fatalf("There is more than one Master registered in Consul.")
		case consulServiceHealthLength == 0:
			log.Infof("No redis services in Consul.")
		default:
			runConf.RunAsSlave(m[0])
		}
	}

}
