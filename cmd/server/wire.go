//go:build wireinject
// +build wireinject

// The build tag makes sure the stub is not built in the final build.

package main

import (
	"log/slog"

	"github.com/apus-run/spine-layout/internal/biz"
	"github.com/apus-run/spine-layout/internal/conf"
	"github.com/apus-run/spine-layout/internal/data"
	"github.com/apus-run/spine-layout/internal/server"
	"github.com/apus-run/spine-layout/internal/service"

	"github.com/apus-run/spine/v3"
	"github.com/google/wire"
)

// wireApp init Spine application.
func wireApp(*conf.Server, *conf.Data, *slog.Logger) (*spine.App, func(), error) {
	panic(wire.Build(server.ProviderSet, data.ProviderSet, biz.ProviderSet, service.ProviderSet, newApp))
}
