package sample

var saved []int

func getUser(id int) int {
	prepare(id)
	return id
}

func prepare(id int) {
	saved = append(saved, id)
}
