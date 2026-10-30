package jwt

// Ports of the remaining JwtReaderTest cases (the others live in jwt_test.go).

import (
	"encoding/base64"
	"testing"

	"github.com/carbynestack/amphora-go/internal/model"
)

const realToken = "eyJhbGciOiJSUzI1NiIsImtpZCI6ImM5Njk0OTgyLWQzMTAtNDBkOC04ZDk4LTczOWI1ZGZjNWUyNiIsInR5cCI6IkpXVCJ9.eyJhbXIiOlsicGFzc3dvcmQiXSwiYXRfaGFzaCI6InowbjhudTNJQ19HaXN3bmFTWjgwZ2ciLCJhdWQiOlsiOGExYTIwNzUtMzY3Yi00ZGU1LTgyODgtMGMyNzQ1OTMzMmI3Il0sImF1dGhfdGltZSI6MTczMTUwMDQ0OSwiZXhwIjoxNzMxNTA0NDIyLCJpYXQiOjE3MzE1MDA4MjIsImlzcyI6Imh0dHA6Ly8xNzIuMTguMS4xMjguc3NsaXAuaW8vaWFtL29hdXRoIiwianRpIjoiZTlhMmQxYzQtZGViNy00MTgwLWE0M2YtN2QwNTZhYjNlNTk3Iiwibm9uY2UiOiJnV1JVZjhxTERaeDZpOFNhMXpMdm9IX2tSQ01OWll2WTE0WTFsLWNBU0tVIiwicmF0IjoxNzMxNTAwODIyLCJzaWQiOiJlNGVkOTc2Mi0yMmNlLTQyYzEtOTU3NC01MDVkYjAyMThhNDYiLCJzdWIiOiJhZmMwMTE3Zi1jOWNkLTRkOGMtYWNlZS1mYTE0MzNjYTBmZGQifQ.OACqa6WjpAeZbHR54b3p7saUk9plTdXlZsou41E-gfC7WxCG7ZEKfDPKXUky-r20oeIt1Ov3S2QL6Kefe5dTXEC6nhKGxeClg8ys56_FPcx_neI-p09_pSWOkMx7DHP65giaP7UubyyInVpE-2Eu1o6TpoheahNQfCahKDsJmJ-4Vvts3wA79UMfOI0WHO4vLaaG6DRAZQK_dv7ltw3p_WlncpaQAtHwY9iVhtdB3LtAI39EjplCoAF0c9uQO6W7KHWUlj24l2kc564bsJgZSrYvezw6b2-FY7YisVnicSyAORpeqhWEpLltH3D8I1NtHlSYMJhWuVZbBhAm7Iz6q1-W-Q9ttvdPchdwPSASFRkrjrdIyQf6MsFrItKzUxYck57EYL4GkyN9MWvMNxL1UTtkzGsFEczUVsJFm8OQpulYXIFZksmnPTBB0KuUUvEZ-xih8V1HsMRoHvbiCLaDJwjOFKzPevVggsSMysPKR52UAZJDZLTeHBnVCtQ3rro6T0RxNg94lXypz0AmfsGnoTF34u4FmMxzoeFZ9N5zmEpOnMRqLs7Sb3FnLL-IMitc9_2bsHuFbBJl8KbiGHBQihK5v5SIa292L7P9ChsxomWVhM29qHNFuXQMwFUr57hmveNh2Fz9mduZ5h2hLUuDf5xc6u9rSxy3_e3t_xBuUT4"

func TestGivenTokenProvided_whenExtractSubject_thenReturnSubject(t *testing.T) {
	got, err := (Reader{Field: "sub"}).UserID("Bearer " + realToken)
	if err != nil || got != "afc0117f-c9cd-4d8c-acee-fa1433ca0fdd" {
		t.Fatal(got, err)
	}
}

func TestUnauthorizedCases(t *testing.T) {
	r := Reader{Field: "sub"}
	noSub := `{"auth_time": 1731500449,
  "exp": 1731504422,
  "iat": 1731500822,
  "something": {
    "what": "is this"
  }}`
	cases := map[string]string{
		"givenInvalidTokenProvided (no sub claim)":       "Bearer header." + base64.StdEncoding.EncodeToString([]byte(noSub)) + ".signature",
		"givenJwtOfInvalidFormat (missing dot)":          "Bearer invalid.jwt_missing_dot_token",
		"givenJwtDataFieldOfInvalidFormat (no Bearer)":   "header." + base64.StdEncoding.EncodeToString([]byte("{")) + ".signature",
		"givenJwtDataFieldOfInvalidFormat (with Bearer)": "Bearer header." + base64.StdEncoding.EncodeToString([]byte("{")) + ".signature",
		"givenInvalidAuthHeader (missing Bearer prefix)": "invalid_auth_header missing Bearer prefix",
	}
	for name, h := range cases {
		_, err := r.UserID(h)
		if k, m := status(err); k != model.KindUnauthorized || m != "No token provided" {
			t.Errorf("%s: %v", name, err)
		}
	}
}
