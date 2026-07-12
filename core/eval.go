package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

func checkArguementCount(args []string, count int) error {
	if len(args) < count {
		return fmt.Errorf("wrong number of arguments")
	}
	return nil
}

func evalPING(args []string) []byte {
	_ = args
	return Encode("PONG")
}

func evalSET(args []string) []byte {
	if err := checkArguementCount(args, 2); err != nil {
		return Encode(err)
	}
	key := args[0]
	value := args[1]
	var durationMs int64 = 0
	for i := 2; i < len(args); i++ {
		if strings.ToUpper(args[i]) == "EX" {
			if i+1 == len(args) {
				return Encode(errors.New("ERR invalid arguments for EX"))
			}
			// Convert expiry time to milliseconds
			// For simplicity, assuming 'args[3]' is in seconds and converting to milliseconds
			// In production, you might want to parse the unit (e.g., if it can be PX for milliseconds)
			ttlSeconds, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil {
				return Encode(errors.New("ERR invalid TTL value"))
			}
			durationMs = ttlSeconds * 1000
		}
	}

	// All string SETs map to the string type with the raw encoding
	// (value held as a Go string / byte representation).
	obj := NewObj(value, durationMs, ObjTypeString, ObjEncodingRaw)
	Put(key, obj)
	return Encode("OK")
}

func evalGET(args []string) []byte {
	if err := checkArguementCount(args, 1); err != nil {
		return Encode(err)
	}
	obj := Get(args[0])
	if obj == nil {
		return Encode(nil)
	}
	return Encode(fmt.Sprintf("%v", obj.Value))
}

func evalTTL(args []string) []byte {
	if err := checkArguementCount(args, 1); err != nil {
		return Encode(err)
	}
	key := args[0]
	obj := Get(key)
	if obj == nil {
		return Encode(-2)
	}
	ttl := (obj.ExpiresAt - time.Now().UnixMilli()) / 1000
	if ttl < 0 {
		ttl = -1
	}
	return Encode(ttl)
}

func evalDEL(args []string) []byte {
	if err := checkArguementCount(args, 1); err != nil {
		return Encode(err)
	}
	counter := 0
	for _, value := range args {
		counter += Del(value)
	}
	return Encode(counter)
}

func evalINCR(args []string) []byte {
	if err := checkArguementCount(args, 1); err != nil {
		return Encode(err)
	}
	key := args[0]
	obj := Get(key)

	// New key: seed it at 1 with string type + int encoding.
	if obj == nil {
		newObj := NewObj(int64(1), 0, ObjTypeString, ObjEncodingInt)
		Put(key, newObj)
		return Encode(int64(1))
	}

	// Existing key: only valid if it is a string encoded as an int.
	if GetType(obj.TypeEncoding) != ObjTypeString || GetEncoding(obj.TypeEncoding) != ObjEncodingInt {
		return Encode(errors.New("ERR value is not an integer or out of range"))
	}

	val, ok := obj.Value.(int64)
	if !ok {
		return Encode(errors.New("ERR value is not an integer or out of range"))
	}
	val++
	obj.Value = val
	return Encode(val)
}

func evalEXPIRE(args []string) []byte {
	if err := checkArguementCount(args, 2); err != nil {
		return Encode(err)
	}
	key := args[0]
	expiry := args[1]
	obj := Get(key)
	if obj == nil {
		return Encode(0)
	}
	expiry_in_secs, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return Encode(errors.New("ERR value is not an integer or out of range"))
	}

	expiry_in_secs = expiry_in_secs * 1000
	obj.ExpiresAt = time.Now().UnixMilli() + expiry_in_secs
	return Encode(1)
}

// EvalAndRespond evaluates a batch of pipelined commands and writes all of
// the encoded responses to the connection in a single write.
func EvalAndRespond(cmds []RedisCmd, c io.ReadWriter) error {
	buf := bytes.NewBuffer(nil)
	for _, cmd := range cmds {
		var resp []byte
		switch cmd.Cmd {
		case "PING":
			resp = evalPING(cmd.Args)
		case "SET":
			resp = evalSET(cmd.Args)
		case "GET":
			resp = evalGET(cmd.Args)
		case "TTL":
			resp = evalTTL(cmd.Args)
		case "DEL":
			resp = evalDEL(cmd.Args)
		case "EXPIRE":
			resp = evalEXPIRE(cmd.Args)
		case "INCR":
			resp = evalINCR(cmd.Args)
		case "BGREWRITEAOF":
			WriteAof()
			resp = Encode("OK")
		default:
			resp = Encode(fmt.Errorf("ERR unknown command '%s'", cmd.Cmd))
		}
		buf.Write(resp)
	}
	_, err := c.Write(buf.Bytes())
	return err
}
