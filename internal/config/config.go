// Package config loads settings from environment variables (12-factor), so
// the same image runs locally, in docker compose and on AKS.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr      string        // FILEFLOW_LISTEN_ADDR (:8080)
	AdminAddr       string        // FILEFLOW_ADMIN_ADDR (:9090) metrics + pprof; never expose publicly
	LogLevel        string        // FILEFLOW_LOG_LEVEL (info)
	LogFormat       string        // FILEFLOW_LOG_FORMAT json|text
	Backend         string        // FILEFLOW_BACKEND local|s3|azure
	MaxUploadBytes  int64         // FILEFLOW_MAX_UPLOAD (100GiB)
	PartSize        int64         // FILEFLOW_PART_SIZE (16MiB) multipart part / Azure block size
	PartConcurrency int           // FILEFLOW_PART_CONCURRENCY (4) parallel parts per upload
	IdleIOTimeout   time.Duration // FILEFLOW_IDLE_IO_TIMEOUT (60s)
	ShutdownTimeout time.Duration // FILEFLOW_SHUTDOWN_TIMEOUT (30s)
	DrainDelay      time.Duration // FILEFLOW_DRAIN_DELAY (5s) readyz=503 before shutdown starts
	EnableH2C       bool          // FILEFLOW_H2C (false) cleartext HTTP/2 behind a TLS-terminating LB

	LocalRoot  string // FILEFLOW_LOCAL_ROOT (./data)
	LocalFsync bool   // FILEFLOW_LOCAL_FSYNC (true)

	S3Endpoint     string // S3_ENDPOINT
	S3Region       string // S3_REGION
	S3Bucket       string // S3_BUCKET
	S3AccessKey    string // S3_ACCESS_KEY
	S3SecretKey    string // S3_SECRET_KEY
	S3PathStyle    bool   // S3_PATH_STYLE
	S3CreateBucket bool   // S3_CREATE_BUCKET

	AzureConnString      string // AZURE_STORAGE_CONNECTION_STRING
	AzureAccountURL      string // AZURE_STORAGE_ACCOUNT_URL
	AzureContainer       string // AZURE_STORAGE_CONTAINER
	AzureCreateContainer bool   // AZURE_CREATE_CONTAINER
}

func Load() (Config, error) {
	var errs []string
	l := loader{errs: &errs}
	c := Config{
		ListenAddr:      l.str("FILEFLOW_LISTEN_ADDR", ":8080"),
		AdminAddr:       l.str("FILEFLOW_ADMIN_ADDR", ":9090"),
		LogLevel:        l.str("FILEFLOW_LOG_LEVEL", "info"),
		LogFormat:       l.str("FILEFLOW_LOG_FORMAT", "json"),
		Backend:         l.str("FILEFLOW_BACKEND", "local"),
		MaxUploadBytes:  l.bytes("FILEFLOW_MAX_UPLOAD", 100<<30),
		PartSize:        l.bytes("FILEFLOW_PART_SIZE", 16<<20),
		PartConcurrency: l.int("FILEFLOW_PART_CONCURRENCY", 4),
		IdleIOTimeout:   l.dur("FILEFLOW_IDLE_IO_TIMEOUT", 60*time.Second),
		ShutdownTimeout: l.dur("FILEFLOW_SHUTDOWN_TIMEOUT", 30*time.Second),
		DrainDelay:      l.dur("FILEFLOW_DRAIN_DELAY", 5*time.Second),
		EnableH2C:       l.bool("FILEFLOW_H2C", false),

		LocalRoot:  l.str("FILEFLOW_LOCAL_ROOT", "./data"),
		LocalFsync: l.bool("FILEFLOW_LOCAL_FSYNC", true),

		S3Endpoint:     l.str("S3_ENDPOINT", ""),
		S3Region:       l.str("S3_REGION", "us-east-1"),
		S3Bucket:       l.str("S3_BUCKET", ""),
		S3AccessKey:    l.str("S3_ACCESS_KEY", ""),
		S3SecretKey:    l.str("S3_SECRET_KEY", ""),
		S3PathStyle:    l.bool("S3_PATH_STYLE", false),
		S3CreateBucket: l.bool("S3_CREATE_BUCKET", false),

		AzureConnString:      l.str("AZURE_STORAGE_CONNECTION_STRING", ""),
		AzureAccountURL:      l.str("AZURE_STORAGE_ACCOUNT_URL", ""),
		AzureContainer:       l.str("AZURE_STORAGE_CONTAINER", ""),
		AzureCreateContainer: l.bool("AZURE_CREATE_CONTAINER", false),
	}
	switch c.Backend {
	case "local", "s3", "azure":
	default:
		errs = append(errs, fmt.Sprintf("FILEFLOW_BACKEND: unknown backend %q", c.Backend))
	}
	if len(errs) > 0 {
		return c, fmt.Errorf("config: %s", strings.Join(errs, "; "))
	}
	return c, nil
}

type loader struct{ errs *[]string }

func (l loader) str(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func (l loader) bool(k string, def bool) bool {
	v, ok := os.LookupEnv(k)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		*l.errs = append(*l.errs, k+": "+err.Error())
	}
	return b
}

func (l loader) int(k string, def int) int {
	v, ok := os.LookupEnv(k)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*l.errs = append(*l.errs, k+": "+err.Error())
	}
	return n
}

func (l loader) dur(k string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(k)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*l.errs = append(*l.errs, k+": "+err.Error())
	}
	return d
}

func (l loader) bytes(k string, def int64) int64 {
	v, ok := os.LookupEnv(k)
	if !ok {
		return def
	}
	n, err := ParseBytes(v)
	if err != nil {
		*l.errs = append(*l.errs, k+": "+err.Error())
	}
	return n
}

// ParseBytes accepts plain numbers or KiB/MiB/GiB/TiB (and KB/MB/GB/TB as
// decimal) suffixes: "16MiB", "100GiB", "5000000".
func ParseBytes(s string) (int64, error) {
	s = strings.TrimSpace(s)
	units := []struct {
		suf  string
		mult int64
	}{
		{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1},
	}
	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u.suf); ok {
			f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
			if err != nil || f < 0 {
				return 0, fmt.Errorf("invalid size %q", s)
			}
			return int64(f * float64(u.mult)), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n, nil
}
