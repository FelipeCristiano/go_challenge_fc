package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// configurações da aplicação
type Config struct {
	HTTP       HTTPConfig
	Database   DatabaseConfig
	SQS        SQSConfig
	Auth       AuthConfig
	Outbox     OutboxConfig
	PendingRef PendingRefConfig
	Shutdown   ShutdownConfig
}

type HTTPConfig struct {
	Port string
}

type DatabaseConfig struct {
	URL string
}

type SQSConfig struct {
	Endpoint          string
	QueueURL          string
	DLQURL            string
	EventsQueueURL    string
	MaxMessages       int32
	VisibilityTimeout int32
	WaitTimeSeconds   int32
	MaxRetries        int
}

type AuthConfig struct {
	Issuer  string
	JWKSURL string
}

type OutboxConfig struct {
	PollInterval time.Duration
	BatchSize    int
	MaxAttempts  int
}

type PendingRefConfig struct {
	PollInterval   time.Duration
	MaxRetries     int
	InitialBackoff time.Duration
}

type ShutdownConfig struct {
	Timeout time.Duration
}

// lê e valida a configuração
func Load() (*Config, error) {
	cfg := &Config{
		HTTP: HTTPConfig{
			Port: getEnvOrDefault("HTTP_PORT", "3000"),
		},
		Database: DatabaseConfig{
			URL: mustGetEnv("DATABASE_URL"),
		},
		SQS: SQSConfig{
			Endpoint:          mustGetEnv("SQS_ENDPOINT"),
			QueueURL:          mustGetEnv("SQS_QUEUE_URL"),
			DLQURL:            mustGetEnv("SQS_DLQ_URL"),
			EventsQueueURL:    getEnvOrDefault("SQS_EVENTS_QUEUE_URL", ""),
			MaxMessages:       int32(getEnvInt("SQS_MAX_MESSAGES", 10)),
			VisibilityTimeout: int32(getEnvInt("SQS_VISIBILITY_TIMEOUT", 60)),
			WaitTimeSeconds:   int32(getEnvInt("SQS_WAIT_TIME", 20)),
			MaxRetries:        getEnvInt("SQS_MAX_RETRIES", 5),
		},
		Auth: AuthConfig{
			Issuer:  mustGetEnv("KEYCLOAK_ISSUER"),
			JWKSURL: mustGetEnv("KEYCLOAK_JWKS_URL"),
		},
		Outbox: OutboxConfig{
			PollInterval: getEnvDuration("OUTBOX_POLL_INTERVAL", 2*time.Second),
			BatchSize:    getEnvInt("OUTBOX_BATCH_SIZE", 50),
			MaxAttempts:  getEnvInt("OUTBOX_MAX_ATTEMPTS", 10),
		},
		PendingRef: PendingRefConfig{
			PollInterval:   getEnvDuration("PENDING_REF_POLL_INTERVAL", 5*time.Second),
			MaxRetries:     getEnvInt("PENDING_REF_MAX_RETRIES", 10),
			InitialBackoff: getEnvDuration("PENDING_REF_INITIAL_BACKOFF", 5*time.Second),
		},
		Shutdown: ShutdownConfig{
			Timeout: getEnvDuration("SHUTDOWN_TIMEOUT", 30*time.Second),
		},
	}

	return cfg, nil
}

func mustGetEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required environment variable %q is not set", key))
	}
	return v
}

func getEnvOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultVal
	}
	return d
}
