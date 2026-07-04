package core

import (
	"errors"
	"fmt"
)

// Encode serializes a value into its RESP-encoded byte representation.
// Supported value types:
//   - string  -> RESP simple string  (+<s>\r\n)
//   - int64   -> RESP integer        (:<n>\r\n)
//   - nil     -> RESP null bulk string ($-1\r\n)
//   - error   -> RESP error          (-<msg>\r\n)
func Encode(v interface{}) []byte {
	switch v := v.(type) {
	case nil:
		return []byte("$-1\r\n")
	case string:
		return []byte(fmt.Sprintf("+%s\r\n", v))
	case int:
		return []byte(fmt.Sprintf(":%d\r\n", v))
	case int64:
		return []byte(fmt.Sprintf(":%d\r\n", v))
	case error:
		return []byte(fmt.Sprintf("-%s\r\n", v.Error()))
	default:
		return []byte(fmt.Sprintf("+%v\r\n", v))
	}
}

// reads the length typically the first integer of the string
// until hit by an non-digit byte and returns
// the integer and the delta = length + 2 (CRLF)
// TODO: Make it simpler and read until we get `\r` just like other functions
func readLength(data []byte) (int, int) {
	pos, length := 0, 0
	for pos = range data {
		b := data[pos]
		if !(b >= '0' && b <= '9') {
			return length, pos + 2
		}
		length = length*10 + int(b-'0')
	}
	return 0, 0
}

func readSimpleString(data []byte) (string, int, error) {
	// first character +
	pos := 1

	for ; data[pos] != '\r'; pos++ {
	}

	return string(data[1:pos]), pos + 2, nil
}

func readError(data []byte) (string, int, error) {
	return readSimpleString(data)
}

func readInt64(data []byte) (int64, int, error) {
	pos := 1
	var value int64 = 0

	for ; data[pos] != '\r'; pos++ {
		value = value*10 + int64(data[pos]-'0')
	}

	return value, pos + 2, nil
}

func readBulkString(data []byte) (string, int, error) {
	pos := 1
	len, delta := readLength(data[pos:])
	pos += delta

	// reading `len` bytes as string
	return string(data[pos:(pos + len)]), pos + len + 2, nil
}

func readArray(data []byte) (interface{}, int, error) {
	pos := 1

	count, delta := readLength(data[pos:])
	pos += delta

	var elems []interface{} = make([]interface{}, count)
	for i := range elems {
		elem, delta, err := DecodeOne(data[pos:])
		if err != nil {
			return nil, 0, err
		}
		elems[i] = elem
		pos += delta
	}
	return elems, pos, nil
}

func DecodeOne(data []byte) (interface{}, int, error) {
	if len(data) == 0 {
		return nil, 0, errors.New("no data")
	}
	switch data[0] {
	case '+':
		return readSimpleString(data)
	case '-':
		return readError(data)
	case ':':
		return readInt64(data)
	case '$':
		return readBulkString(data)
	case '*':
		return readArray(data)
	}
	return nil, 0, nil
}

func Decode(data []byte) ([]interface{}, error) {
	var command_array []interface{}
	if len(data) == 0 {
		return nil, errors.New("no data")
	}
	for len(data) > 0 {
		value, delta, err := DecodeOne(data)
		if err != nil {
			return nil, errors.New("no data")
		}
		command_array = append(command_array, value)
		data = data[delta:]
	}
	return command_array, nil
}

func DecodeArrayString(data []byte) ([][]string, error) {
	var command_array [][]string
	value_array, err := Decode(data)
	if err != nil {
		return nil, err
	}
	for _, elem := range value_array {
		ts := elem.([]interface{})
		tokens := make([]string, len(ts))
		for i := range tokens {
			tokens[i] = ts[i].(string)
		}
		command_array = append(command_array, tokens)
	}

	return command_array, nil
}
