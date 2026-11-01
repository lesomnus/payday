package pdgen_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// notified is `src` in an app that declares notifications, with payday's
// entities and the notification records copied in.
func notified(t *testing.T, src string) string {
	t.Helper()
	return "import \"google/protobuf/any.proto\";\n" +
		"option (payday.app) = {notify: {notification: 40, receipt: 41, keep: {days: 30}}};\n" +
		ownAll(t) + notificationCopies(t, 40, 41) + src
}

// TestANotificationPayloadIsReadWithWhatItHolds: the messages that say they
// are what a notification carries, nested ones too, and a field said to be
// about no one, in the payload or in a message it holds -- which no message
// but an entity's own may otherwise say (payday#193).
func TestANotificationPayloadIsReadWithWhatItHolds(t *testing.T) {
	x := require.New(t)

	s, err := readAs(t, "fleet", notified(t, `
message JobDone {
  option (payday.notification) = {};
  bytes job = 1;
  string outcome = 2 [(payday.field) = {personal: false}];
  Where at = 3;
  google.protobuf.Timestamp when = 4;

  message Retried {
    option (payday.notification) = {};
    bytes job = 1;
  }
}
message Where {
  string bay = 1 [(payday.field) = {personal: false}];
}
`))
	x.NoError(err)
	x.Len(s.Payloads, 2)
	x.Equal("fleet.JobDone", string(s.Payloads[0].FullName()))
	x.Equal("fleet.JobDone.Retried", string(s.Payloads[1].FullName()))

	// And a message no payload holds is held to what it was.
	_, err = readAs(t, "fleet", notified(t, `
message Where {
  string bay = 1 [(payday.field) = {personal: false}];
}
`))
	x.ErrorContains(err, "is read on an entity's own columns")
}

// TestANotificationPayloadNamesPeopleByIdentifierOnly: a declaration in it of
// anything but `personal: false`, a row, a contract's message and an Any are
// each a person, or a way to hold one, that nothing here would follow.
func TestANotificationPayloadNamesPeopleByIdentifierOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want string
	}{
		"a field declared personal": {`
message JobDone {
  option (payday.notification) = {};
  string keeper = 1 [(payday.field) = {personal: true}];
}`, "fleet.JobDone.keeper: (payday.field) in a notification payload says more than `personal: false`"},

		"a secret": {`
message JobDone {
  option (payday.notification) = {};
  bytes token = 1 [(payday.field) = {secret: true}];
}`, "fleet.JobDone.token: (payday.field) in a notification payload says more than `personal: false`"},

		"one a message it holds declares personal": {`
message JobDone {
  option (payday.notification) = {};
  repeated Hand by = 1;
}
message Hand {
  string name = 1 [(payday.field) = {personal: true}];
}`, "fleet.Hand.name: (payday.field) in a notification payload, which notification payload fleet.JobDone holds"},

		"an entity": {`
message JobDone {
  option (payday.notification) = {};
  Holder by = 1;
}`, "holds fleet.Holder, an entity, in a notification payload"},

		"an Any": {`
message JobDone {
  option (payday.notification) = {};
  map<string, google.protobuf.Any> extra = 1;
}`, "holds a google.protobuf.Any, in a notification payload"},

		"an entity that says it is one": {`
message Bay {
  option (payday.notification) = {};
  bytes id = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  option (orm.message) = {rpc: {get: {}}};
  option (payday.entity) = {domain: 30, global: {}, erase: {hard: {}}};
}`, "fleet.Bay: says it is a notification payload, and is an entity"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readAs(t, "fleet", notified(t, tc.src))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestANotificationPayloadNeedsNotificationsToCarryIt.
func TestANotificationPayloadNeedsNotificationsToCarryIt(t *testing.T) {
	_, err := readAs(t, "fleet", ownAll(t)+`
message JobDone {
  option (payday.notification) = {};
  bytes job = 1;
}`)
	require.ErrorContains(t, err, "fleet.JobDone: says it is a notification payload, and this app declares no notifications")
}
