package relay

import (
	"os"

	"github.com/naoina/toml"
)

type Config struct {
	HTTPRelays []HTTPConfig `toml:"http"`
}

type HTTPConfig struct {
	// Name identifies the HTTP relay
	Name string `toml:"name"`

	// consistent nodes replicas
	Replicas int `toml:"replicas"`

	// Addr should be set to the desired listening host:port
	Addr string `toml:"bind-addr"`

	// Set certificate in order to handle HTTPS requests
	SSLCombinedPem string `toml:"ssl-combined-pem"`

	// Default retention policy to set for forwarded requests
	DefaultRetentionPolicy string `toml:"default-retention-policy"`

	// Outputs is a list of backed servers where read or writes will be forwarded
	Outputs map[string][]HTTPOutputConfig `toml:"output"`

	// Former is a list of former backed where servers read or write
	Former map[string][]HTTPOutputConfig `toml:"former"`
}

type HTTPOutputConfig struct {
	// Name of the backend server
	Name string `toml:"name"`

	// Location should be set to the URL of the backend server's write endpoint
	Location string `toml:"location"`

	// Timeout sets a per-backend timeout for write requests. (Default 10s)
	// The format used is the same seen in time.ParseDuration
	Timeout string `toml:"timeout"`

	Interval string `toml:"interval"`

	// Buffer failed writes up to maximum count. (Default 0, retry/buffering disabled)
	BufferSizeMB int `toml:"buffer-size-mb"`

	// Maximum batch size in KB (Default 512)
	MaxBatchKB int `toml:"max-batch-kb"`

	// Maximum delay between retry attempts.
	// The format used is the same seen in time.ParseDuration (Default 10s)
	MaxDelayInterval string `toml:"max-delay-interval"`

	// Skip TLS verification in order to use self signed certificate.
	// WARNING: It's insecure. Use it only for developing and don't use in production.
	SkipTLSVerification bool `toml:"skip-tls-verification"`
}

// LoadConfigFile parses the specified file into a Config object
func LoadConfigFile(filename string) (cfg Config, err error) {
	f, err := os.Open(filename)
	if err != nil {
		return cfg, err
	}
	defer f.Close()

	return cfg, toml.NewDecoder(f).Decode(&cfg)
}
