package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Config holds all the runtime configuration
type Config struct {
	Verbose          bool
	GrpcPort         uint16
	DatabaseHost     string
	DatabasePort     uint16
	DatabaseUsername string
	DatabasePassword string
	DatabaseName     string
}

func LoadConfig(logger *slog.Logger) (*Config, error) {
	// Define the list of required environment variables
	required := []string{
		"CIVIL_DB_USER",
		"CIVIL_DB_PASS",
		"CIVIL_DB_NAME",
	}

	// Loop through and check for missing ones
	var missing []string
	for _, key := range required {
		if os.Getenv(key) == "" {
			missing = append(missing, key)
		}
	}

	// If any are missing, return a detailed error
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	// Return the populated config struct
	// You can also set defaults here for optional vars (like Port)
	return &Config{
		Verbose:          getVerboseEnv(logger),
		GrpcPort:         getPortEnv("CIVIL_GRPC_PORT", 50051, logger),
		DatabaseHost:     os.Getenv("CIVIL_DB_HOST"),
		DatabasePort:     getPortEnv("CIVIL_DB_PORT", 5432, logger),
		DatabaseUsername: os.Getenv("CIVIL_DB_USER"),
		DatabasePassword: os.Getenv("CIVIL_DB_PASS"),
		DatabaseName:     os.Getenv("CIVIL_DB_NAME"),
	}, nil
}

// Helper for optional variables
func getEnv(key string, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getPortEnv(key string, fallback uint16, logger *slog.Logger) uint16 {
	if value, exists := os.LookupEnv(key); exists {
		var intValue, err = strconv.ParseUint(value, 10, 16)

		if err != nil {
			logger.Warn("Failure in parsing integer. Falling back to default", slog.Any("error", err), slog.Int("applied_default", int(fallback)))
			return fallback
		}

		return uint16(intValue)
	} else {
		return fallback
	}

}

func getVerboseEnv(logger *slog.Logger) bool {
	if value, exists := os.LookupEnv("CIVIL_VERBOSE"); exists {
		boolValue, err := strconv.ParseBool(value)

		if err != nil {
			logger.Warn("Failure in parsing CIVIL_VERBOSE. Falling back to default", slog.Any("error", err), slog.Bool("applied_default", false))
			return false
		}

		return boolValue
	}

	return false
}
