package main

import (
	"flag"
	"fmt"
	"go-redis-server/config"
	"go-redis-server/server"
	"log"
)

func setupFlags() {
	flag.StringVar(&config.Host, "host", "0.0.0.0", "host for the go-redis-server")
	flag.IntVar(&config.Port, "port", 6379, "port for the go-redis-server")
	flag.Parse()
}

func main() {
	setupFlags()
	log.Println("starting go-redis-server on", fmt.Sprintf("%s:%d", config.Host, config.Port))
	err := server.RunAsyncTCPServerDarwin()
	if err != nil {
		log.Println("err", err)
	}
}
