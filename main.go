package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {

	log.Println("[INFO] Start!")

	// init the config
	resec := defaultConfig()

	resec.redisClientInit()

	resec.consulClientInit()

	go resec.redisHealthCheck()
	go resec.watchForMaster()

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)

	//select {
	//case <-c:
	//	log.Printf("[INFO] Caught signal, releasing lock and stopping...")
	//	resec.Stop()
	//}


	for {
		select {
		case <- c:
			log.Printf("[INFO] Caught signal, releasing lock and stopping...")
			resec.Stop()
		case health := <- resec.redisHealthCh:
			if health.Healthy {

			} else {

			}
		case currentMaster := <- resec.masterConsulServiceCh:
			// Use master node address if it's registered without service address
			if currentMaster.Service.ID == resec.consulServiceId {
				continue
			}
			go resec.waitForLock()
			var masterAddress string
			if currentMaster.Service.Address != "" {
				masterAddress = currentMaster.Service.Address
			} else {
				masterAddress = currentMaster.Node.Address
			}
			resec.runAsSlave(masterAddress)
			go resec.waitForLock()
		case lockStatus := <- resec.lockChannel:
			// of lock state error ?
			// if lock accuiered ?
			// if lock canceled

			resec.runAsMaster()
		}
	}
}
