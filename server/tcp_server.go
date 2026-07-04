package server

import (
	"io"
	"log"
	"net"
	"strconv"
	"strings"

	"go-redis-server/config"
	"go-redis-server/core"
)

func readCommand(c io.ReadWriter) ([]core.RedisCmd, error) {
	// TODO: Max read in one shot is 512 bytes
	// To allow input > 512 bytes, then repeated read until
	// we get EOF or designated delimiter
	var buf []byte = make([]byte, 512)
	n, err := c.Read(buf[:])
	if err != nil {
		return nil, err
	}
	array_of_commands, err := core.DecodeArrayString(buf[:n])
	if err != nil {
		return nil, err
	}
	// NOTE: capacity (not length!) — make with a length would pre-fill
	// the slice with zero-value RedisCmd{Cmd:""} entries, which then get
	// shifted in front of the real commands and produce phantom
	// "unknown command ''" replies that desync redis-cli's reply stream.
	redis_commands := make([]core.RedisCmd, 0, len(array_of_commands))

	for _, commands := range array_of_commands {
		redis_commands = append(redis_commands,
			core.RedisCmd{
				Cmd:  strings.ToUpper(commands[0]),
				Args: commands[1:],
			})
	}

	return redis_commands, nil
}

func RunSyncTCPServer() {
	log.Println("starting a synchronous TCP server on", config.Host, config.Port)

	var con_clients int = 0

	// listening to the configured host:port
	lsnr, err := net.Listen("tcp", config.Host+":"+strconv.Itoa(config.Port))
	if err != nil {
		log.Println("err", err)
		return
	}

	for {
		// blocking call: waiting for the new client to connect
		c, err := lsnr.Accept()
		if err != nil {
			log.Println("err", err)
		}

		// increment the number of concurrent clients
		con_clients += 1

		for {
			// over the socket, continuously read the command and print it out
			cmds, err := readCommand(c)
			if err != nil {
				c.Close()
				con_clients -= 1
				if err == io.EOF {
					break
				}
				log.Println("err", err)
				break
			}
			// Evaluate all pipelined commands and write all responses
			// to the connection in a single write.
			core.EvalAndRespond(cmds, c)
		}
	}
}
