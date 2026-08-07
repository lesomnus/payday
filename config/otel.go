package config

import (
	"context"
	"fmt"

	"github.com/lesomnus/mkot"
	"github.com/lesomnus/mkot/mkotx"
	"github.com/lesomnus/mkot/pretty"
	"github.com/lesomnus/otx"
	"github.com/lesomnus/z"
	"go.opentelemetry.io/otel/attribute"
)

// Service is who the telemetry is from.
//
// The template this came from wrote all three of these out as literals, which
// is the part that could not come along: a span attributed to whichever app was
// copied first is a span nobody can find, and nothing about it looks wrong
// until somebody goes looking.
type Service struct {
	// Name is what every span, metric and log this process emits is attributed
	// to. It is the app's name, which is what a [Loader] already carries:
	//
	//	config.Service{Name: l.Name(), ...}
	Name string

	// Version is what is running. Nothing is nothing said -- the attribute is
	// left off rather than written empty, since a resource claiming version ""
	// is a claim and no version at all is the truth about a build that did not
	// stamp one.
	Version string

	// Scope is the Go package instruments are created from, e.g.
	// "github.com/acme/thing".
	//
	// Without it, an instrument is attributed to whichever library happened to
	// create it, and a dashboard ends up grouping by the name of a dependency.
	Scope string
}

// OtelConfig is what an app is told about its own telemetry, in the
// OpenTelemetry Collector's own configuration language.
//
// It is inlined rather than nested so that a file written for the collector
// reads here as it does there.
type OtelConfig struct {
	mkot.Config `yaml:",inline"`
}

// Build makes every signal the configuration describes, attributed to `svc`,
// and answers with a context carrying them.
//
// What the configuration says nothing about is filled in: a resource naming the
// service, somewhere to print to, and a provider for each of the three signals.
// Each one is only added if the file did not name it, so a deployment that
// described its own pipeline gets that and not this on top of it.
func (c *OtelConfig) Build(ctx context.Context, svc Service) (context.Context, *otx.Otx, error) {
	if svc.Name == "" {
		return nil, nil, fmt.Errorf("the service has to be named: telemetry attributed to nothing is telemetry nobody can find")
	}

	// A nil configuration is a configuration that said nothing, which is a
	// meaning worth having: an app that holds this by pointer and never read a
	// file still gets the defaults below rather than a panic.
	otc := &mkot.Config{}
	if c != nil {
		otc = &c.Config
	}

	if otc.Processors == nil {
		otc.Processors = map[mkot.Id]mkot.ProcessorConfig{}
	}
	if otc.Exporters == nil {
		otc.Exporters = map[mkot.Id]mkot.ExporterConfig{}
	}
	if otc.Providers == nil {
		otc.Providers = map[mkot.Id]*mkot.ProviderConfig{}
	}

	// Named after the service so that an app which does describe its own
	// pipeline can refer to this one, and so that two of them in one file --
	// which is what a collector configuration lifted from elsewhere looks like
	// -- do not become each other.
	resource := mkot.Id("resource/" + svc.Name)
	if _, ok := otc.Processors[resource]; !ok {
		attrs := []mkot.Attr{
			{Key: "service.name", Value: attribute.StringValue(svc.Name)},
		}
		if svc.Version != "" {
			attrs = append(attrs, mkot.Attr{Key: "service.version", Value: attribute.StringValue(svc.Version)})
		}

		otc.Processors[resource] = &mkot.Resource{Attributes: attrs}
	}
	if _, ok := otc.Exporters["pretty"]; !ok {
		otc.Exporters["pretty"] = pretty.ExporterConfig{}
	}
	if _, ok := otc.Providers["tracer"]; !ok {
		otc.Providers["tracer"] = &mkot.ProviderConfig{
			Processors: []mkot.Id{resource},
		}
	}
	if _, ok := otc.Providers["meter"]; !ok {
		otc.Providers["meter"] = &mkot.ProviderConfig{
			Processors: []mkot.Id{resource},
		}
	}
	if _, ok := otc.Providers["logger"]; !ok {
		otc.Providers["logger"] = &mkot.ProviderConfig{
			Exporters: []mkot.Id{"pretty"},
		}
	}

	opts := []otx.Option{}
	if svc.Scope != "" {
		opts = append(opts, otx.WithScopeName(svc.Scope))
	}

	// Every signal the configuration describes, in one value. A signal it says
	// nothing about is off rather than an error.
	o, err := mkotx.FromConfig(ctx, otc, "", opts...)
	if err != nil {
		return nil, nil, z.Err(err, "build telemetry")
	}

	return otx.Into(ctx, o), o, nil
}
