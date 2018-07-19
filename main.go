package main

import (
	"log"
)

func main() {
	log.Println("[INFO] Start!")

	resec, err := setup()
	if err != nil {
		log.Fatal(err)
	}
	resec.waitForRedisToBeReady()

	go resec.watchRedisReplicationStatus()
	go resec.watchConsulMasterService()

	defer func() {
		log.Printf("[INFO] Shutting down ...")
		resec.stop()
	}()

	resec.start()
}
