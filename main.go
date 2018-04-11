package main

import (
	"log"
)

func main() {
	log.Println("[INFO] Start!")

	resec := setup()

	go resec.watchRedisReplicationStatus()
	go resec.watchConsulMasterService()

	defer func() {
		log.Printf("[INFO] Shutting down ...")
		resec.stop()
	}()

	resec.start()
}
