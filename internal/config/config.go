package config

import (
	"os"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Config holds the application settings
type Config struct {
	PhpMyAdminEnabled bool
	PhpMyAdminDomain  string
	// Fraction of the WordPress container limits used as requests
	RequestCPUFraction    float64
	RequestMemoryFraction float64
}

// AppConfig is the global instance accessible by other packages
var AppConfig Config
var logger = log.Log.WithName("config")

// Load initializes the configuration
func Load() {
	AppConfig = Config{}

	AppConfig.PhpMyAdminEnabled = os.Getenv("PHPMYADMIN_ENABLED") == "true"
	AppConfig.RequestCPUFraction = getFraction("REQUEST_CPU_FRACTION", 0.25)
	AppConfig.RequestMemoryFraction = getFraction("REQUEST_MEMORY_FRACTION", 0.5)

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

// getFraction reads a ratio in (0, 1] from the environment, exiting on
// invalid values so a typo cannot silently change every site's requests
func getFraction(key string, fallback float64) float64 {
	raw := getEnv(key, "")
	if raw == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f <= 0 || f > 1 {
		logger.Info("invalid fraction, must be a number in (0, 1]", "variable", key, "value", raw)
		os.Exit(1)
	}
	return f
}
