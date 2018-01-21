package main

import (
	"log"
)

func main() {

	log.Println("[INFO] Start!")

	// init the Init
	resec := Init()

	go resec.RedisHealthCheck()

	go resec.WatchForMaster()

	defer resec.Stop()

	resec.Start()

}
