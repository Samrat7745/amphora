package config

import (
	"reflect"
	"testing"
)

// Matrix of missing/invalid environment values with the exact error text, per variable of config.go.

func with(kv ...string) Getenv {
	m := base()
	for i := 0; i < len(kv); i += 2 {
		if kv[i+1] == "<unset>" {
			delete(m, kv[i])
		} else {
			m[kv[i]] = kv[i+1]
		}
	}
	return env(m)
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name string
		get  Getenv
		want string
	}{
		{"port not a number", with("AMPHORA_SERVICE_PORT", "http"), `invalid AMPHORA_SERVICE_PORT="http"`},
		{"port float", with("AMPHORA_SERVICE_PORT", "80.5"), `invalid AMPHORA_SERVICE_PORT="80.5"`},
		{"port overflow", with("AMPHORA_SERVICE_PORT", "99999999999999999999"), `invalid AMPHORA_SERVICE_PORT="99999999999999999999"`},
		{"opening timeout not a number", with("AMPHORA_OPENING_TIMEOUT", "5s"), `invalid AMPHORA_OPENING_TIMEOUT="5s"`},
		{"player id not a number", with("PLAYER_ID", "one"), `invalid PLAYER_ID="one"`},
		{"redis port not a number", with("REDIS_PORT", "abc"), `invalid REDIS_PORT="abc"`},
		{"db port not a number", with("DB_PORT", "pg"), `invalid DB_PORT="pg"`},
		{"ssl flag invalid", with("NO_SSL_VALIDATION", "maybe"), `invalid NO_SSL_VALIDATION="maybe"`},
		{"compat flag invalid", with("COMPAT_JAVA_TAG_REPLACE_BUG", "2"), `invalid COMPAT_JAVA_TAG_REPLACE_BUG="2"`},
		{"mac key missing", with("MAC_KEY", "<unset>"), `MAC_KEY: Invalid value "" for type BigInteger`},
		{"mac key not numeric", with("MAC_KEY", "0xff"), `MAC_KEY: Invalid value "0xff" for type BigInteger`},
		{"mac key float", with("MAC_KEY", "1.5"), `MAC_KEY: Invalid value "1.5" for type BigInteger`},
		{"mac key whitespace", with("MAC_KEY", " 12"), `MAC_KEY: Invalid value " 12" for type BigInteger`},
		{"prime missing", with("SPDZ_PRIME", "<unset>"), `SPDZ_PRIME: Invalid value "" for type BigInteger`},
		{"prime garbage", with("SPDZ_PRIME", "abc"), `SPDZ_PRIME: Invalid value "abc" for type BigInteger`},
		{"r missing", with("SPDZ_R", "<unset>"), `SPDZ_R: Invalid value "" for type BigInteger`},
		{"r garbage", with("SPDZ_R", "1e3"), `SPDZ_R: Invalid value "1e3" for type BigInteger`},
		{"rinv missing", with("SPDZ_R_INV", "<unset>"), `SPDZ_R_INV: Invalid value "" for type BigInteger`},
		{"rinv garbage", with("SPDZ_R_INV", "--1"), `SPDZ_R_INV: Invalid value "--1" for type BigInteger`},
		// only the first error is reported (in the order the variables are read)
		{"first error wins", with("AMPHORA_SERVICE_PORT", "x", "MAC_KEY", "y"), `invalid AMPHORA_SERVICE_PORT="x"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := LoadFrom(c.get)
			if err == nil || cfg != nil || err.Error() != c.want {
				t.Fatalf("got (%v, %v), want error %q", cfg, err, c.want)
			}
		})
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name string
		get  Getenv
		want string
	}{
		{"redis host missing", with("REDIS_HOST", "<unset>"), "REDIS_HOST and REDIS_PORT are required"},
		{"redis port missing", with("REDIS_PORT", "<unset>"), "REDIS_HOST and REDIS_PORT are required"},
		{"redis port zero", with("REDIS_PORT", "0"), "REDIS_HOST and REDIS_PORT are required"},
		{"minio missing", with("MINIO_ENDPOINT", "<unset>"), "MINIO_ENDPOINT is required"},
		{"db user missing", with("DB_USER", "<unset>"), "DB_USER is required"},
		{"db user empty", with("DB_USER", ""), "DB_USER is required"},
		{"negative player", with("PLAYER_ID", "-1"), "PLAYER_ID must not be negative"},
		{"partner without scheme", with("AMPHORA_VC_PARTNERS", "partner.example.com:8080"), `Invalid service address "partner.example.com:8080"`},
		{"partner without host", with("AMPHORA_VC_PARTNERS", "http://ok:1,http://"), `Invalid service address "http://"`},
		{"partner garbage", with("AMPHORA_VC_PARTNERS", "http://a b"), `Invalid service address "http://a b"`},
		{"castor missing", with("CASTOR_SERVICE_URI", "<unset>"), "CASTOR_SERVICE_URI is required"},
		{"castor without scheme", with("CASTOR_SERVICE_URI", "castor:10100"), `Invalid service address "castor:10100" (CASTOR_SERVICE_URI)`},
		{"castor bare host", with("CASTOR_SERVICE_URI", "castor.default.svc"), `Invalid service address "castor.default.svc" (CASTOR_SERVICE_URI)`},
		{"castor invalid url", with("CASTOR_SERVICE_URI", "http://[::1"), `Invalid service address "http://[::1" (CASTOR_SERVICE_URI)`},
		// checks run in a fixed order: redis before minio before db user
		{"order", with("REDIS_HOST", "<unset>", "DB_USER", "<unset>"), "REDIS_HOST and REDIS_PORT are required"},
		{"order 2", with("MINIO_ENDPOINT", "<unset>", "DB_USER", "<unset>"), "MINIO_ENDPOINT is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := LoadFrom(c.get)
			if err != nil {
				t.Fatalf("load must succeed: %v", err)
			}
			if err := cfg.Validate(); err == nil || err.Error() != c.want {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestEveryVariableIsRead(t *testing.T) {
	m := map[string]string{
		"AMPHORA_SERVICE_PORT": "8080", "AMPHORA_USER_ID_FIELD_NAME": "preferred_username.x", "AMPHORA_OPA_DEFAULT_POLICY_PACKAGE": "pkg.a",
		"AMPHORA_OPA_URL": "http://opa:8181", "MAC_KEY": "-5", "SPDZ_PRIME": "17", "SPDZ_R": "3", "SPDZ_R_INV": "6",
		"AMPHORA_VC_PARTNERS": " http://a:1 ,,http://b:2,", "AMPHORA_OPENING_TIMEOUT": "123", "PLAYER_ID": "1", "NO_SSL_VALIDATION": "TRUE",
		"TRUSTED_CERTIFICATES": "/a.pem, /b.pem", "CASTOR_SERVICE_URI": "https://castor:1", "REDIS_HOST": "rh", "REDIS_PORT": "6380",
		"MINIO_ENDPOINT": "http://m:9000", "DB_HOST": "dbh", "DB_PORT": "5433", "DB_NAME": "other", "DB_USER": "du", "DB_PASSWORD": "dp",
		"COMPAT_JAVA_TAG_REPLACE_BUG": "true", "LOG_LEVEL": "debug",
	}
	c, err := LoadFrom(env(m))
	if err != nil || c.Validate() != nil {
		t.Fatal(err, c.Validate())
	}
	if c.Port != 8080 || c.UserIDField != "preferred_username.x" || c.OpaPolicyPackage != "pkg.a" || c.OpaURL != "http://opa:8181" ||
		c.MacKey.Int64() != -5 || c.Prime.Int64() != 17 || c.R.Int64() != 3 || c.RInv.Int64() != 6 ||
		!reflect.DeepEqual(c.VCPartners, []string{"http://a:1", "http://b:2"}) || c.OpeningTimeoutMs != 123 || c.PlayerID != 1 || !c.NoSSLValidation ||
		!reflect.DeepEqual(c.TrustedCertificates, []string{"/a.pem", "/b.pem"}) || c.CastorServiceURI != "https://castor:1" ||
		c.RedisHost != "rh" || c.RedisPort != 6380 || c.MinioEndpoint != "http://m:9000" || c.DBHost != "dbh" || c.DBPort != 5433 ||
		c.DBName != "other" || c.DBUser != "du" || c.DBPassword != "dp" || !c.JavaTagReplaceBug || c.LogLevel != "DEBUG" {
		t.Fatalf("%+v", c)
	}
}

func TestDefaultValues(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{"MAC_KEY": "1", "SPDZ_PRIME": "2", "SPDZ_R": "3", "SPDZ_R_INV": "4"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != 10000 || c.UserIDField != "sub" || c.OpaPolicyPackage != "carbynestack.def" || c.OpaURL != "http://opa.carbynestack.io" ||
		c.OpeningTimeoutMs != 5000 || c.PlayerID != 0 || c.NoSSLValidation || c.DBHost != "localhost" || c.DBPort != 5432 || c.DBName != "amphora" ||
		c.LogLevel != "INFO" || c.JavaTagReplaceBug || c.InputMaskStore != "input-mask-store" || c.InterimValueStore != "interim-value-store" ||
		c.MinioBucket != "minio-amphora-bucket" || c.MinioAccessKey != "dummy-key" || c.MinioSecretKey != "dummy-secret" ||
		len(c.VCPartners) != 0 || len(c.TrustedCertificates) != 0 || c.InterimKeyPrefix() != "interim-value-store::" {
		t.Fatalf("%+v", c)
	}
	// an empty value counts as unset for variables with defaults
	c, err = LoadFrom(env(map[string]string{"MAC_KEY": "1", "SPDZ_PRIME": "2", "SPDZ_R": "3", "SPDZ_R_INV": "4", "AMPHORA_SERVICE_PORT": "", "LOG_LEVEL": "", "DB_NAME": ""}))
	if err != nil || c.Port != 10000 || c.LogLevel != "INFO" || c.DBName != "amphora" {
		t.Fatal(c, err)
	}
	// whitespace around numbers and booleans is tolerated
	c, err = LoadFrom(env(map[string]string{"MAC_KEY": "1", "SPDZ_PRIME": "2", "SPDZ_R": "3", "SPDZ_R_INV": "4", "AMPHORA_SERVICE_PORT": " 81 ", "NO_SSL_VALIDATION": " true "}))
	if err != nil || c.Port != 81 || !c.NoSSLValidation {
		t.Fatal(c, err)
	}
}

func TestBooleanSpellings(t *testing.T) {
	for _, s := range []string{"1", "t", "T", "TRUE", "true", "True"} {
		c, err := LoadFrom(with("NO_SSL_VALIDATION", s))
		if err != nil || !c.NoSSLValidation {
			t.Fatalf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"0", "f", "F", "FALSE", "false", "False"} {
		c, err := LoadFrom(with("NO_SSL_VALIDATION", s))
		if err != nil || c.NoSSLValidation {
			t.Fatalf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"yes", "no", "on", "tRuE", "-1"} {
		if _, err := LoadFrom(with("NO_SSL_VALIDATION", s)); err == nil {
			t.Fatalf("%q accepted", s)
		}
	}
}

func TestParseBigIntegerMatrix(t *testing.T) {
	good := map[string]string{"0": "0", "-0": "0", "+7": "7", "-170141183460469231731687303715884105727": "-170141183460469231731687303715884105727",
		"340282366920938463463374607431768211456": "340282366920938463463374607431768211456", "007": "7"}
	for in, want := range good {
		v, err := ParseBigInteger(in)
		if err != nil || v.String() != want {
			t.Fatalf("%q: %v %v", in, v, err)
		}
	}
	for _, in := range []string{"", " ", "1 ", "1_0", "0b1", "0x1", "1e3", "1.0", "--1", "+-1", "٣", "NaN", "１２"} {
		if _, err := ParseBigInteger(in); err == nil || err.Error() != `Invalid value "`+in+`" for type BigInteger` {
			t.Fatalf("%q: %v", in, err)
		}
	}
}
