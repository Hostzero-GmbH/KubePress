package config

import (
	"os"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Config holds the application settings
type Config struct {
	PhpMyAdminEnabled bool
	PhpMyAdminDomain  string
}

// AppConfig is the global instance accessible by other packages
var AppConfig Config
var logger = log.Log.WithName("config")

// Load initializes the configuration
func Load() {
	AppConfig = Config{}

	AppConfig.PhpMyAdminEnabled = os.Getenv("PHPMYADMIN_ENABLED") == "true"

	if AppConfig.PhpMyAdminEnabled {
		PHPMyAdminDomain := os.Getenv("PHPMYADMIN_DOMAIN")
		if PHPMyAdminDomain == "" {
			logger.Info("PHPMYADMIN_DOMAIN variable is not set. This needs to be set to enable phpMyAdmin access.")
			os.Exit(1)
		}
	}

}

func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}
