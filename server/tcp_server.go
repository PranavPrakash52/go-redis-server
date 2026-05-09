package server

import (
	"io"
	"log"
	"net"
	"strconv"

	"go-redis-server/config"
	"go-redis-server/core"
)

func readCommand(c net.Conn) (core.Command, error) {
	// TODO: Max read in one shot is 512 bytes
	// To allow input > 512 bytes, then repeated read until
	// we get EOF or designated delimiter
	var buf []byte = make([]byte, 512)
	n, err := c.Read(buf[:])
	if err != nil {
		return core.Command{}, err
	}

	decoded, err := core.Decode(buf[:n])
	if err != nil {
		return core.Command{}, err
	}
	// Convert array of interface string type to slice of strings
	string_decoded := make([]string, len(decoded.([]interface{})))
	for i, v := range decoded.([]interface{}) {
		string_decoded[i] = v.(string)
	}
	return core.Command{
		Cmd:  string_decoded[0],
		Args: string_decoded[1:],
	}, nil

}

func respond(cmd core.Command, c net.Conn) {
	encoded_cmd, err := core.Encode(cmd)
	if err != nil {
		c.Write([]byte("-ERR unknown command\r\n"))
	}
	c.Write(encoded_cmd)
}

func RunSyncTCPServer() {
	log.Println("starting a synchronous TCP server on", config.Host, config.Port)

	var con_clients int = 0

	// listening to the configured host:port
	lsnr, err := net.Listen("tcp", config.Host+":"+strconv.Itoa(config.Port))
	if err != nil {
		panic(err)
	}

	for {
		// blocking call: waiting for the new client to connect
		c, err := lsnr.Accept()
		if err != nil {
			panic(err)
		}

		// increment the number of concurrent clients
		con_clients += 1
		log.Println("client connected with address:", c.RemoteAddr(), "concurrent clients", con_clients)

		for {
			// over the socket, continuously read the command and print it out
			cmd, err := readCommand(c)
			if err != nil {
				c.Close()
				con_clients -= 1
				log.Println("client disconnected", c.RemoteAddr(), "concurrent clients", con_clients)
				if err == io.EOF {
					break
				}
				log.Println("err", err)
			}
			log.Println("command", cmd)
			respond(cmd, c)
		}
	}
}
