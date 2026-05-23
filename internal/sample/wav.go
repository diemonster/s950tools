package sample

// scaleToInt16 normalises a decoded SIGNED integer sample to signed 16-bit.
// The caller is responsible for translating unsigned-source formats (8-bit
// WAV is unsigned 0..255) into the signed domain before calling here.
func scaleToInt16(v int, srcBits int) int32 {
	switch {
	case srcBits == 16:
		return int32(int16(v))
	case srcBits == 8:
		return int32(int8(v)) << 8
	case srcBits == 24:
		x := int32(v)
		if x&0x800000 != 0 {
			x |= ^0xFFFFFF
		}
		return x >> 8
	case srcBits == 32:
		return int32(v >> 16)
	default:
		// Best-effort linear scale for any other bit depth.
		max := int64(1) << uint(srcBits-1)
		if max == 0 {
			return int32(v)
		}
		return int32((int64(v) * 32767) / max)
	}
}

func clampInt16(v int32) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}
