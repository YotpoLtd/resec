package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {

	// init the config
	resec := defaultConfig()

	resec.redisClientInit()

	resec.consulClientInit()

	go resec.redisHealthCheck()

	go resec.Run()

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)

	select {
	case <-c:
		log.Printf("[INFO] Caught signal, releasing lock and stopping...")
		resec.Stop()
	}

}
