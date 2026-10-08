package parsing

import (
	"reflect"
	"testing"
)

func TestExtractFileRecordsCallsInSourceOrder(t *testing.T) {
	t.Parallel()

	file, err := testExtractor(t, "go").ExtractFile("users.go", []byte(`package sample

func buildUsers() {
	users := loadUsers()
	users = loadUsers()
	accounts := loadAccounts()
	store.Users()
	user.Save()
	fmt.Println(users, accounts)
}

func loadUsers() {}
func loadAccounts() {}
`))
	if err != nil {
		t.Fatalf("ExtractFile() error = %v", err)
	}

	got := callSpellings(file.Calls)
	want := []string{
		"loadUsers", "loadUsers", "loadAccounts",
		"store.Users", "user.Save", "fmt.Println",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("call spellings = %#v, want %#v", got, want)
	}
	for _, call := range file.Calls {
		if call.EndByte <= call.StartByte {
			t.Fatalf("call %#v has an empty span", call)
		}
	}
}

func callSpellings(calls []CallRecord) []string {
	spellings := make([]string, 0, len(calls))
	for _, call := range calls {
		spellings = append(spellings, call.Ref.Spelling())
	}
	return spellings
}

func functionNamed(t *testing.T, units []CodeUnit, name string) CodeUnit {
	t.Helper()
	for _, unit := range units {
		if unit.Kind == CodeKindFunction && unit.Name == name {
			return unit
		}
	}
	t.Fatalf("missing function %q", name)
	return CodeUnit{}
}
