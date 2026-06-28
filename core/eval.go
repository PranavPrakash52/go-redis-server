package core

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

func checkArguementCount(args []string, count int, c io.ReadWriter) error {
	if len(args) < count {
		c.Write([]byte("-ERR invalid arguments from the couter\r\n"))
		return nil
	}
	return nil
}

func evalPING(c io.ReadWriter) error {
	c.Write([]byte("+PONG\r\n"))
	return nil
}
func evalSET(args []string, c io.ReadWriter) error {
	checkArguementCount(args, 2, c)
	key := args[0]
	value := args[1]
	var durationMs int64 = 0
	for i := 2; i < len(args); i++ {
		if strings.ToUpper(args[i]) == "EX" {
			if i+1 == len(args) {
				c.Write([]byte("-ERR invalid arguments for EX\r\n"))
				return nil
			}
			// Convert expiry time to milliseconds
			// For simplicity, assuming 'args[3]' is in seconds and converting to milliseconds
			// In production, you might want to parse the unit (e.g., if it can be PX for milliseconds)
			var err error
			var ttlSeconds int64
			ttlSeconds, err = strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				c.Write([]byte("-ERR invalid TTL value\r\n"))
				return err
			}
			durationMs = ttlSeconds * 1000
		}
	}

	obj := NewObj(value, durationMs)
	Put(key, obj)
	c.Write([]byte("+OK\r\n"))
	return nil
}
func evalGET(args []string, c io.ReadWriter) error {
	checkArguementCount(args, 1, c)
	obj := Get(args[0])
	if obj == nil {
		c.Write([]byte("-nil\r\n"))
		return nil
	}
	c.Write([]byte(fmt.Sprintf("+%v\r\n", obj.Value)))
	return nil
}
func evalTTL(args []string, c io.ReadWriter) error {
	checkArguementCount(args, 1, c)
	key := args[0]
	obj := Get(key)
	if obj == nil {
		c.Write([]byte(fmt.Sprintf("+%v\r\n", -2)))
		return nil
	}
	ttl := (obj.ExpiresAt - time.Now().UnixMilli()) / 1000
	if ttl < 0 {
		ttl = -1
	}
	c.Write([]byte(fmt.Sprintf("+%v\r\n", ttl)))
	return nil
}

func evalDEL(args []string, c io.ReadWriter) error {
	couter := 0
	checkArguementCount(args, 2, c)
	for _, value := range args {
		couter += Del(value)
	}
	c.Write([]byte(fmt.Sprintf("+%v\r\n", couter)))
	return nil
}

func evalEXPIRE(args []string, c io.ReadWriter) error {
	checkArguementCount(args, 1, c)
	key := args[0]
	expiry := args[1]
	obj := Get(key)
	if obj == nil {
		c.Write([]byte(":0\r\n"))
		return nil
	}
	expiry_in_secs, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return errors.New("(error) ERR value is not an integer or out of range")
	}

	expiry_in_secs = expiry_in_secs * 1000
	obj.ExpiresAt = time.Now().UnixMilli() + expiry_in_secs
	c.Write([]byte(":1\r\n"))
	return nil
}

func EvalAndRespond(cmd *RedisCmd, c io.ReadWriter) error {
	switch cmd.Cmd {
	case "PING":
		return evalPING(c)
	case "SET":
		return evalSET(cmd.Args, c)
	case "GET":
		return evalGET(cmd.Args, c)
	case "TTL":
		return evalTTL(cmd.Args, c)
	case "DEL":
		return evalDEL(cmd.Args, c)
	case "EXPIRE":
		return evalEXPIRE(cmd.Args, c)
	default:
		return evalPING(c)
	}
}
