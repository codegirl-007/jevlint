package bad

func Normalize(value int) int {
	if value < 0 {
		value = -value
	}
	if value > 100 {
		value = 100
	}
	return value
}
