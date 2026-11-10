package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/apus-run/spine-layout/internal/conf"

	"github.com/apus-run/spine/contrib/otel/v3/tracing"
	"github.com/apus-run/spine/v3"
	"github.com/apus-run/spine/v3/config"
	"github.com/apus-run/spine/v3/config/env"
	"github.com/apus-run/spine/v3/config/file"
	"github.com/apus-run/spine/v3/log"
	"github.com/apus-run/spine/v3/transport/grpc"
	"github.com/apus-run/spine/v3/transport/http"

	_ "go.uber.org/automaxprocs"
)

// go build -ldflags "-X main.Version=x.y.z"
var (
	// Name is the name of the compiled software.
	Name = "spine-layout"
	// Version is the version of the compiled software.
	Version string
	// flagconf is the config flag.
	flagconf string

	id, _ = os.Hostname()
)

func init() {
	flag.StringVar(&flagconf, "conf", "../../configs", "config path, eg: -conf config.yaml")
}

func newApp(logger *slog.Logger, gs *grpc.Server, hs *http.Server) *spine.App {
	return spine.New(
		spine.ID(id),
		spine.Name(Name),
		spine.Version(Version),
		spine.Metadata(map[string]string{}),
		spine.Logger(logger),
		spine.Server(
			gs,
			hs,
		),
	)
}

func main() {
	flag.Parse()
	logger := log.NewLogger(
		slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			AddSource: true,
			Level:     slog.LevelInfo,
		}),
		log.WithExtractor(tracing.TraceAttrs),
	).With(
		slog.String("service.id", id),
		slog.String("service.name", Name),
		slog.String("service.version", Version),
	)
	log.SetDefault(logger)
	c := config.New(
		config.WithSource(
			file.NewSource(flagconf),
			env.NewSource("KRATOS"),
			env.NewSource("SPINE_"),
		),
	)
	defer c.Close()

	if err := c.Load(); err != nil {
		panic(err)
	}

	var bc conf.Bootstrap
	if err := c.Scan(&bc); err != nil {
		panic(err)
	}

	app, cleanup, err := wireApp(bc.Server, bc.Data, logger)
	if err != nil {
		panic(err)
	}
	defer cleanup()

	// start and wait for stop signal
	if err := app.Run(); err != nil {
		panic(err)
	}
}
