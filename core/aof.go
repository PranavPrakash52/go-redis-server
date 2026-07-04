package core

import (
	"fmt"
	"go-redis-server/config"
	"log"
	"os"
	"strings"
)

func WriteAof() {
	aof_file, err := os.OpenFile(config.AofFilename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatal(err)
	}
	for key, obj := range store {
		cmd := fmt.Sprintf("SET %s %s", key, obj.Value)
		tokens := strings.Split(cmd, " ")
		aof_file.Write(Encode(tokens))
	}
	log.Println("log compaction complete")
}
