package main

import (
	"log"

	resec "github.com/YotpoLtd/resec/resec"
)

func main() {
	log.Println("[INFO] Start!")

	app, err := resec.Setup()
	if err != nil {
		log.Fatal(err)
	}

	app.WaitForRedisToBeReady()
	go app.WatchRedisUptime()
	go app.WatchRedisReplicationStatus()
	go app.WatchConsulMasterService()

	app.Run()
}
