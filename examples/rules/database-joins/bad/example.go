package bad

type User struct {
	Name   string
	TeamID int
}

type Team struct {
	ID   int
	Name string
}

type UserWithTeam struct {
	UserName string
	TeamName string
}

type Store interface {
	Users() []User
	Teams() []Team
}

func LoadUsersWithTeams(store Store) []UserWithTeam {
	users := store.Users()
	teams := store.Teams()
	joined := make([]UserWithTeam, 0, len(users))
	for _, user := range users {
		for _, team := range teams {
			if team.ID == user.TeamID {
				joined = append(joined, UserWithTeam{UserName: user.Name, TeamName: team.Name})
			}
		}
	}
	return joined
}
