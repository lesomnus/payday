package pdgen_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/internal/pdgen"
)

// What `pd gen` holds an app's accounts to (payday#184): the holder field it
// names has to be the identifier of one, by its name and its number both, and
// a method may be an account's own only in an app that has accounts -- each a
// resolver that would otherwise serve a call as somebody it is not, quietly.

// accounted is payday's tenant and holder, the holder with `field` added as an
// overlay would add it, and `app` and `svc` beside them.
func accounted(app, field, svc string) string {
	return app + tenantOf("payday") + `
message Holder {
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias = 4;
  string name = 5;
  string desc = 6;
  map<string, string> labels = 7;
  google.protobuf.Timestamp date_updated = 13 [(orm.field) = {version: {}}];
  google.protobuf.Timestamp date_erased = 14 [(orm.field) = {erased: {}}];
  google.protobuf.Timestamp date_created = 15 [(orm.field) = {immutable: true, default: ""}];
  string role = 8;
` + field + `
  option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 2, own: OWN_HOLDER};
}
` + svc
}

const accountField = `  bytes account = 9 [(orm.field) = {type: TYPE_UUID, nullable: true}];`

const accountsAt9 = `option (payday.app) = {accounts: {field: {name: "account", number: 9}}};`

const accept = `
message Answer {}

service InvitationService {
  rpc Accept(Answer) returns (Answer) {
    option (payday.method) = {account: {}};
  }
  rpc Revoke(Answer) returns (Answer);
}
`

func TestAccountsAreReadOffTheHolderFieldTheAppNames(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "payday", accounted(accountsAt9, accountField, accept))
	x.NoError(err)
	x.NotNil(s.Accounts)
	x.Equal("account", s.Accounts.Field.Name())
	x.Equal([]pdgen.AccountMethod{{
		Full:    "/payday.InvitationService/Accept",
		Service: "InvitationService",
		Method:  "Accept",
		Const:   "InvitationService_Accept_FullMethodName",
		At:      "test/schema.proto",
	}}, s.Accounts.Methods, "the one marked, and not the one beside it")

	// A holder field that may be null, or may not: a membership of nobody's
	// is one nobody signs in to through an account.
	_, err = readAs(t, "payday", accounted(accountsAt9, `  bytes account = 9 [(orm.field) = {type: TYPE_UUID}];`, ""))
	x.NoError(err)

	// And an app that says nothing has none.
	s, err = readAs(t, "payday", accounted("", accountField, ""))
	x.NoError(err)
	x.Nil(s.Accounts)
}

func TestAnAccountFieldThatIsNotOneIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name  string
		app   string
		field string
		says  string
	}{
		{
			"a name the number does not have",
			`option (payday.app) = {accounts: {field: {name: "acount", number: 9}}};`, accountField,
			`followed a rename`,
		},
		{
			"the name alone",
			`option (payday.app) = {accounts: {field: {name: "account"}}};`, accountField,
			`both its name and its number`,
		},
		{
			"a number nothing holds",
			`option (payday.app) = {accounts: {field: {name: "account", number: 12}}};`, accountField,
			`has nothing at 12`,
		},
		{
			"a string",
			accountsAt9, `  string account = 9;`,
			`is string`,
		},
		{
			"bytes that are not an identifier",
			accountsAt9, `  bytes account = 9;`,
			`is bytes`,
		},
		{
			"an edge",
			accountsAt9, `  Tenant account = 9 [(orm.edge) = {nullable: true}];`,
			`is an edge`,
		},
		{
			"the holder's own key",
			`option (payday.app) = {accounts: {field: {name: "id", number: 1}}};`, accountField,
			`own key`,
		},
		{
			"one declared personal",
			accountsAt9, `  bytes account = 9 [(orm.field) = {type: TYPE_UUID, nullable: true}, (payday.field) = {personal: true}];`,
			`declared personal`,
		},
		{
			"one declared secret",
			accountsAt9, `  bytes account = 9 [(orm.field) = {type: TYPE_UUID, nullable: true}, (payday.field) = {secret: true}];`,
			`declared secret`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readAs(t, "payday", accounted(tc.app, tc.field, ""))
			require.ErrorContains(t, err, tc.says)
			require.ErrorContains(t, err, "accounts: field")
		})
	}
}

// TestAMethodIsAnAccountsOwnOnlyWhereThereAreAccounts: one marked in an app
// whose holders are each one tenant's has nobody to be served as, and a
// declaration that says nothing is one somebody relies on.
func TestAMethodIsAnAccountsOwnOnlyWhereThereAreAccounts(t *testing.T) {
	x := require.New(t)

	_, err := readAs(t, "payday", accounted("", accountField, accept))
	x.ErrorContains(err, "/payday.InvitationService/Accept is marked an account's own")
	x.ErrorContains(err, "declares no accounts")

	_, err = readAs(t, "payday", accounted(accountsAt9, accountField, `
message Answer {}

service InvitationService {
  rpc Accept(Answer) returns (Answer) {
    option (payday.method) = {};
  }
}
`))
	x.ErrorContains(err, "(payday.method) says nothing")
}

// TestAnAccountFieldNoIndexServesIsWarnedOf: every call an account makes is
// resolved by it, so without one each reads every holder there is.
func TestAnAccountFieldNoIndexServesIsWarnedOf(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "payday", accounted(accountsAt9, accountField, ""))
	x.NoError(err)
	x.Condition(func() bool {
		for _, w := range pdgen.Warnings(s) {
			if strings.Contains(w, "accounts: no index of payday.Holder begins with account") {
				return true
			}
		}
		return false
	}, "%v", pdgen.Warnings(s))

	indexed := strings.Replace(accounted(accountsAt9, accountField, ""),
		`option (orm.message) = {rpc: {crud: true}};
  option (payday.entity) = {domain: 2, own: OWN_HOLDER};`,
		`option (orm.message) = {rpc: {crud: true}, indexes: [{name: "by_account", refs: [{name: "account", number: 9}]}]};
  option (payday.entity) = {domain: 2, own: OWN_HOLDER};`, 1)
	s, err = readAs(t, "payday", indexed)
	x.NoError(err)
	for _, w := range pdgen.Warnings(s) {
		x.NotContains(w, "accounts:")
	}
}
