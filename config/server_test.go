package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lesomnus/payday/config"
	"github.com/lesomnus/payday/grpcx"
)

func TestServerConfig(t *testing.T) {
	t.Run("nothing said is nothing given, so gRPC keeps its own", func(t *testing.T) {
		x := require.New(t)

		c := config.ServerConfig{}
		x.Empty(c.GrpcOptions())

		// And what a caller may reach for is off: an unwritten field is a
		// deployment that did not ask, and neither of these is served to
		// somebody who did not.
		x.False(c.AllowReflection)
		x.NotNil(c.Closed())
	})
	t.Run("what is said is given", func(t *testing.T) {
		x := require.New(t)

		c := config.ServerConfig{
			MaxRecvMsgSize:       1 << 20,
			MaxConcurrentStreams: 100,
			Keepalive: config.KeepaliveConfig{
				MaxConnectionAge: 30 * time.Minute,
				MinTime:          10 * time.Second,
			},
		}

		// A size, a stream limit, the parameters and the policy.
		x.Len(c.GrpcOptions(), 4)
	})
	t.Run("an address nobody gave is one somebody can reach", func(t *testing.T) {
		x := require.New(t)

		// The alternative to a default here is a listener on whichever port
		// the kernel handed out, which serves and answers and is reachable by
		// nobody.
		x.Equal(config.DefaultAddr, config.ServerConfig{}.ListenAddr())
		x.Equal(":1234", config.ServerConfig{Addr: ":1234"}.ListenAddr())
	})
	t.Run("the usual cap is what nothing written down means", func(t *testing.T) {
		x := require.New(t)

		// Zero is the ordinary answer, so a field nobody filled in is the
		// ordinary answer -- and saying "cap nothing" is a thing said, which
		// is what a negative one is for.
		x.Equal(grpcx.DefaultTimeout, config.ServerConfig{}.CallTimeout())
		x.Equal(time.Minute, config.ServerConfig{Timeout: time.Minute}.CallTimeout())
		x.Negative(config.ServerConfig{Timeout: -1}.CallTimeout())
	})
	t.Run("no rate is no limiter", func(t *testing.T) {
		x := require.New(t)

		// Not a limiter that allows everything -- nothing, so the chain a
		// deployment that said nothing is served with is the one it had.
		x.Nil(config.ServerConfig{}.Limiter())
		x.Nil(config.ServerConfig{Limit: config.LimitConfig{Burst: 10}}.Limiter())
	})
	t.Run("a rate with no burst is one second's worth", func(t *testing.T) {
		x := require.New(t)

		x.Equal(20, config.LimitConfig{Rate: 20}.BurstOr())
		// Rounded up, since a burst is a whole number of calls and rounding
		// down a rate below one would refuse everything.
		x.Equal(1, config.LimitConfig{Rate: 0.5}.BurstOr())
		x.Equal(40, config.LimitConfig{Rate: 20, Burst: 40}.BurstOr())

		x.NotNil(config.ServerConfig{Limit: config.LimitConfig{Rate: 20}}.Limiter())
	})
	t.Run("the environment says the same things the file does", func(t *testing.T) {
		x := require.New(t)

		type Config struct {
			Server config.ServerConfig `yaml:"server"`
		}

		var c Config
		_, err := acme.OverrideFromEnv(&c, []string{
			"ACME_SERVER_ALLOW_REFLECTION=true",
			"ACME_SERVER_MAX_RECV_MSG_SIZE=1048576",
			"ACME_SERVER_KEEPALIVE_MAX_CONNECTION_AGE=30m",
			"ACME_SERVER_LIMIT_RATE=50",
		})
		x.NoError(err)

		x.True(c.Server.AllowReflection)
		x.Equal(1<<20, c.Server.MaxRecvMsgSize)
		x.Equal(30*time.Minute, c.Server.Keepalive.MaxConnectionAge)
		// The number a deployment is most likely to want to move without
		// building an image, which is why it is worth a line here.
		x.Equal(float64(50), c.Server.Limit.Rate)
	})
}

func TestHttpConfig(t *testing.T) {
	t.Run("no address is no second listener", func(t *testing.T) {
		x := require.New(t)

		x.False(config.HttpConfig{}.Serves())
		x.True(config.HttpConfig{Addr: ":8080"}.Serves())
	})
	t.Run("a page nobody named is not answered for", func(t *testing.T) {
		x := require.New(t)

		// Nothing rather than a rule that says no, since that is what tells
		// the wiring there is no cross-origin story here at all -- which is
		// not "nothing works": a page on this same origin needs no answer.
		x.Nil(config.HttpConfig{}.Origin())

		is := config.HttpConfig{Origins: []string{"http://localhost:5173"}}.Origin()
		x.NotNil(is)
		x.True(is("http://localhost:5173"))
		x.False(is("https://somewhere.else"))
	})
	t.Run("a star is every page on the internet", func(t *testing.T) {
		x := require.New(t)

		is := config.HttpConfig{Origins: []string{"*"}}.Origin()
		x.True(is("https://anywhere.at.all"))
	})
}
