package main

import (
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func BuildPoolConfig(env *Config) (*pgxpool.Config, error) {
	// Start with an empty/default config
	config, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, err
	}

	// Mutate the connection config safely
	config.ConnConfig.Host = env.DatabaseHost
	config.ConnConfig.Port = env.DatabasePort
	config.ConnConfig.User = env.DatabaseUsername
	config.ConnConfig.Password = env.DatabasePassword
	config.ConnConfig.Database = env.DatabaseName

	// Hardcoded constraints required by the intended civil architecture
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	config.ConnConfig.TLSConfig = nil

	return config, nil
}
