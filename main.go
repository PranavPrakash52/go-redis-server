package main

import (
	"flag"
	"go-redis-server/config"
	"go-redis-server/server"
	"log"
)

func setupFlags() {
	flag.StringVar(&config.Host, "host", "0.0.0.0", "host for the dice server")
	flag.IntVar(&config.Port, "port", 7379, "port for the dice server")
	flag.Parse()
}

func main() {
	setupFlags()
	log.Println("***** genesis *****")
	err := server.RunAsyncTCPServerDarwin()
	if err != nil {
		log.Println("err", err)
	}
}
