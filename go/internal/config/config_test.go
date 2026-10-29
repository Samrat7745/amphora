package config

import "testing"

func env(m map[string]string) Getenv { return func(k string) string { return m[k] } }

func base() map[string]string {
	return map[string]string{"MAC_KEY": "-1234", "SPDZ_PRIME": "17", "SPDZ_R": "3", "SPDZ_R_INV": "6",
		"REDIS_HOST": "r", "REDIS_PORT": "6379", "MINIO_ENDPOINT": "http://m:9000", "DB_USER": "u",
		"CASTOR_SERVICE_URI": "http://castor:10100", "AMPHORA_VC_PARTNERS": "http://a:1, http://b:2"}
}

func TestDefaults(t *testing.T) {
	c, err := LoadFrom(env(base()))
	if err != nil || c.Validate() != nil {
		t.Fatal(err, c.Validate())
	}
	if c.Port != 10000 || c.UserIDField != "sub" || c.OpeningTimeoutMs != 5000 || c.DBName != "amphora" || len(c.VCPartners) != 2 {
		t.Fatalf("%+v", c)
	}
	if c.MacKey.String() != "-1234" || c.InputMaskKeyPrefix() != "input-mask-store::" {
		t.Fatal("mac/prefix")
	}
}

func TestInvalid(t *testing.T) {
	m := base()
	m["SPDZ_PRIME"] = ""
	if _, err := LoadFrom(env(m)); err == nil {
		t.Fatal("empty prime must fail")
	}
	m = base()
	m["CASTOR_SERVICE_URI"] = "castor.default.svc.cluster.local"
	c, _ := LoadFrom(env(m))
	if c.Validate() == nil {
		t.Fatal("scheme-less castor uri must fail")
	}
	m = base()
	m["AMPHORA_VC_PARTNERS"] = "nohost"
	c, _ = LoadFrom(env(m))
	if c.Validate() == nil {
		t.Fatal("partner without host must fail")
	}
}
