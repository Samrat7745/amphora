// Command e2erun uploads tuples to Castor and runs the end-to-end flow against Amphora nodes.
// Usage: e2erun -amphora u0,u1 -castor c0,c1 -keys k0,k1 [-upload] [-secret uuid] [-words n] [-out file]
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/Samrat7745/amphora/go/internal/e2e"
	"github.com/Samrat7745/amphora/go/internal/testkit"
)

func main() {
	am := flag.String("amphora", "", "comma separated Amphora base URLs (index = player id)")
	ca := flag.String("castor", "", "comma separated Castor base URLs (for -upload)")
	keys := flag.String("keys", "1234567,7654321", "comma separated MAC key shares")
	upload := flag.Bool("upload", false, "upload fresh tuples first")
	n := flag.Int("tuples", 2000, "tuples per type to upload")
	secret := flag.String("secret", "82a73814-321c-4261-abcd-27c6c0ebfb00", "secret id")
	words := flag.Int("words", 3, "words per secret")
	seed := flag.Int64("seed", 1, "seed for plaintext values")
	out := flag.String("out", "", "write transcript JSON here")
	flag.Parse()
	var mk []*big.Int
	for _, k := range strings.Split(*keys, ",") {
		v, _ := new(big.Int).SetString(k, 10)
		mk = append(mk, v)
	}
	sp := testkit.Params()
	if *upload {
		for _, t := range []string{"INPUT_MASK_GFP", "MULTIPLICATION_TRIPLE_GFP"} {
			if err := testkit.UploadTuples(sp, strings.Split(*ca, ","), mk, t, *n); err != nil {
				fmt.Fprintln(os.Stderr, "upload:", err)
				os.Exit(2)
			}
		}
		fmt.Println("tuples uploaded")
	}
	if *am == "" {
		return
	}
	jwt := "Bearer " + base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		strings.NewReplacer("-", "+", "_", "/").Replace(base64.RawStdEncoding.EncodeToString([]byte(`{"sub":"alice"}`))) + ".sig"
	res, err := e2e.Run(e2e.Config{Amphoras: strings.Split(*am, ","), MacKeys: mk, Sp: sp, SecretID: *secret, Words: *words, Seed: *seed, Token: jwt})
	if *out != "" {
		b, _ := json.MarshalIndent(res.Steps, "", " ")
		_ = os.WriteFile(*out, b, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAILED:", err)
		os.Exit(1)
	}
	fmt.Println("e2e OK:", len(res.Steps), "steps")
}
