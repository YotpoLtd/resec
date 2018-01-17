package main

import (
	"log"
)

func main() {

	log.Println("[INFO] Start!")

	// init the Config
	resec := Config()

	resec.RedisClientInit()

	resec.ConsulClientInit()

	go resec.RedisHealthCheck()

	go resec.WatchForMaster()

	defer resec.Stop()

	resec.Start()

}
