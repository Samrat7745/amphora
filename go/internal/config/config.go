// Package config parses the environment (same variable names as the Java service, profile k8s).
package config

import (
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime settings.
type Config struct {
	Port                int
	UserIDField         string
	OpaPolicyPackage    string
	OpaURL              string
	MacKey              *big.Int
	Prime, R, RInv      *big.Int
	VCPartners          []string
	OpeningTimeoutMs    int64
	PlayerID            int
	NoSSLValidation     bool
	TrustedCertificates []string
	CastorServiceURI    string

	RedisHost          string
	RedisPort          int
	InputMaskStore     string
	InterimValueStore  string
	MinioEndpoint      string
	MinioBucket        string
	MinioAccessKey     string
	MinioSecretKey     string
	DBHost, DBUser     string
	DBPassword, DBName string
	DBPort             int
	LogLevel           string
	// JavaTagReplaceBug (extension COMPAT_JAVA_TAG_REPLACE_BUG, default true) reproduces the Java service's
	// always-failing PUT /tags; set it to false to let PUT /tags replace the tags.
	JavaTagReplaceBug bool
}

// InvalidBigIntegerMsg is BigIntegerConverter.INVALID_VALUE_EXCEPTION_MSG.
const InvalidBigIntegerMsg = "Invalid value \"%s\" for type BigInteger"

// ParseBigInteger is BigIntegerConverter.convert: empty or malformed input is an error.
func ParseBigInteger(s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || s == "" {
		return nil, fmt.Errorf(InvalidBigIntegerMsg, s)
	}
	return v, nil
}

// Getenv abstracts os.Getenv for tests.
type Getenv func(string) string

// Load reads the configuration from the process environment.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads the configuration via get and validates it like Spring's boot does.
func LoadFrom(get Getenv) (*Config, error) {
	c := &Config{
		InputMaskStore: "input-mask-store", InterimValueStore: "interim-value-store",
		MinioBucket: "minio-amphora-bucket", MinioAccessKey: "dummy-key", MinioSecretKey: "dummy-secret",
	}
	var err error
	fail := func(e error) {
		if err == nil {
			err = e
		}
	}
	def := func(k, d string) string {
		if v := get(k); v != "" {
			return v
		}
		return d
	}
	num := func(k string, d int64) int64 {
		s := def(k, strconv.FormatInt(d, 10))
		v, e := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if e != nil {
			fail(fmt.Errorf("invalid %s=%q", k, s))
		}
		return v
	}
	booleanDef := func(k, d string) bool {
		s := strings.TrimSpace(def(k, d))
		v, e := strconv.ParseBool(s)
		if e != nil {
			fail(fmt.Errorf("invalid %s=%q", k, s))
		}
		return v
	}
	boolean := func(k string) bool {
		s := strings.TrimSpace(def(k, "false"))
		v, e := strconv.ParseBool(s)
		if e != nil {
			fail(fmt.Errorf("invalid %s=%q", k, s))
		}
		return v
	}
	bigint := func(k string) *big.Int {
		v, e := ParseBigInteger(get(k))
		if e != nil {
			fail(fmt.Errorf("%s: %w", k, e))
			return new(big.Int)
		}
		return v
	}
	c.Port = int(num("AMPHORA_SERVICE_PORT", 10000))
	c.UserIDField = def("AMPHORA_USER_ID_FIELD_NAME", "sub")
	c.OpaPolicyPackage = def("AMPHORA_OPA_DEFAULT_POLICY_PACKAGE", "carbynestack.def")
	c.OpaURL = def("AMPHORA_OPA_URL", "http://opa.carbynestack.io")
	c.MacKey = bigint("MAC_KEY")
	c.Prime = bigint("SPDZ_PRIME")
	c.R = bigint("SPDZ_R")
	c.RInv = bigint("SPDZ_R_INV")
	c.VCPartners = splitList(get("AMPHORA_VC_PARTNERS"))
	c.OpeningTimeoutMs = num("AMPHORA_OPENING_TIMEOUT", 5000)
	c.PlayerID = int(num("PLAYER_ID", 0))
	c.NoSSLValidation = boolean("NO_SSL_VALIDATION")
	c.TrustedCertificates = splitList(get("TRUSTED_CERTIFICATES"))
	c.CastorServiceURI = get("CASTOR_SERVICE_URI")
	c.RedisHost = get("REDIS_HOST")
	c.RedisPort = int(num("REDIS_PORT", 0))
	c.MinioEndpoint = get("MINIO_ENDPOINT")
	c.DBHost = def("DB_HOST", "localhost")
	c.DBPort = int(num("DB_PORT", 5432))
	c.DBName = def("DB_NAME", "amphora") // extension: Java fixes the name to amphora
	c.DBUser = get("DB_USER")
	c.DBPassword = get("DB_PASSWORD")
	// Default true: behave like the Java service (PUT /secret-shares/{id}/tags always fails with 500).
	c.JavaTagReplaceBug = booleanDef("COMPAT_JAVA_TAG_REPLACE_BUG", "true")
	c.LogLevel = strings.ToUpper(def("LOG_LEVEL", "INFO"))
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks the settings Java requires at boot.
func (c *Config) Validate() error {
	if c.RedisHost == "" || c.RedisPort == 0 {
		return fmt.Errorf("REDIS_HOST and REDIS_PORT are required")
	}
	if c.MinioEndpoint == "" {
		return fmt.Errorf("MINIO_ENDPOINT is required")
	}
	if c.DBUser == "" {
		return fmt.Errorf("DB_USER is required")
	}
	if c.PlayerID < 0 {
		return fmt.Errorf("PLAYER_ID must not be negative")
	}
	for _, p := range c.VCPartners {
		u, err := url.Parse(p)
		if err != nil || u.Host == "" {
			return fmt.Errorf("Invalid service address %q", p)
		}
	}
	if c.CastorServiceURI != "" {
		if u, err := url.Parse(c.CastorServiceURI); err != nil || u.Host == "" {
			return fmt.Errorf("Invalid service address %q (CASTOR_SERVICE_URI)", c.CastorServiceURI)
		}
	} else {
		return fmt.Errorf("CASTOR_SERVICE_URI is required")
	}
	return nil
}

// InputMaskKeyPrefix is Spring CacheKeyPrefix.simple() applied to the input mask store name.
func (c *Config) InputMaskKeyPrefix() string { return c.InputMaskStore + "::" }

// InterimKeyPrefix is the key prefix of the interim value store.
func (c *Config) InterimKeyPrefix() string { return c.InterimValueStore + "::" }

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
