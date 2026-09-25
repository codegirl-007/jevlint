package good

type JoinedRecord struct {
	UserName string
	TeamName string
}

type Queryer interface {
	Query(string) []JoinedRecord
}

func LoadUsersWithTeams(database Queryer) []JoinedRecord {
	return database.Query("SELECT users.name, teams.name FROM users JOIN teams ON teams.id = users.team_id")
}
