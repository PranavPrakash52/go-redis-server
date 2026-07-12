package core

// Object types occupy the high nibble (first 4 bits) of Obj.TypeEncoding.
// Only string is supported for now
const (
	ObjTypeString uint8 = 0
)

// Object encodings occupy the low nibble (last 4 bits) of Obj.TypeEncoding.
//   - raw: the value is stored as a Go string (the byte representation).
//   - int: the value is stored as an int64.
const (
	ObjEncodingRaw uint8 = 0
	ObjEncodingInt uint8 = 1
)

// SetTypeEncoding packs a type (high 4 bits) and an encoding (low 4 bits)
// into a single byte. This is how both are stored on Obj.
func SetTypeEncoding(typ, enc uint8) uint8 {
	return (typ << 4) | (enc & 0x0F)
}

// GetType extracts the object type from a packed type-encoding byte.
func GetType(te uint8) uint8 {
	return te >> 4
}

// GetEncoding extracts the object encoding from a packed type-encoding byte.
func GetEncoding(te uint8) uint8 {
	return te & 0x0F
}
