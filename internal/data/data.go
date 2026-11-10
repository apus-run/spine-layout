package data

import (
	"context"
	"fmt"

	"github.com/apus-run/spine-layout/internal/conf"
	"github.com/apus-run/spine-layout/internal/data/ent"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/apus-run/spine/v3/log"
	_ "github.com/go-sql-driver/mysql"
	"github.com/google/wire"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(NewData, NewTodoRepo)

// Data holds the long-lived storage clients shared by repos.
type Data struct {
	db *ent.Client
}

// NewData opens the database client and returns it with a cleanup function.
func NewData(c *conf.Data) (*Data, func(), error) {
	dc := c.GetDatabase()
	maxIdle, maxOpen := int(dc.GetMaxIdleConns()), int(dc.GetMaxOpenConns())
	if maxIdle < 0 || maxOpen < 0 {
		return nil, nil, fmt.Errorf("database pool limits must be nonnegative")
	}
	if maxIdle == 0 {
		maxIdle = 10
	}
	if maxOpen == 0 {
		maxOpen = 20
	}
	driver, err := entsql.Open(dc.GetDriver(), dc.GetSource())
	if err != nil {
		return nil, nil, err
	}
	driver.DB().SetMaxOpenConns(maxOpen)
	driver.DB().SetMaxIdleConns(min(maxIdle, maxOpen))
	db := ent.NewClient(ent.Driver(driver))
	if dc.GetDebug() {
		db = db.Debug()
	}
	// Auto migration is a convenience for local development. In production,
	// apply schema changes as a separate reviewed step instead.
	if dc.GetAutoMigrate() {
		if err := db.Schema.Create(context.Background()); err != nil {
			db.Close()
			return nil, nil, err
		}
	}
	cleanup := func() {
		log.Info("closing the data resources")
		if err := db.Close(); err != nil {
			log.Error("failed closing the database", "err", err)
		}
	}
	return &Data{db: db}, cleanup, nil
}
