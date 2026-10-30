// Command epdrive runs an MPC program through two Ephemeral players (secretParams mode): it secret-shares the inputs
// with the players' MAC key shares, sends the activation to both players concurrently and checks the plaintext result.
// Usage: epdrive -urls http://gw0,http://gw1 -hosts h0,h1 -keys 1234567,7654321 -values 100,122 -expect 222
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/carbynestack/amphora-go/internal/testkit"
)

const program = `listen(10000)
client_socket_id = regint()
acceptclientconnection(client_socket_id, 10000)
v = sint.read_from_socket(client_socket_id, %d)
sum = v[0]
for i in range(1, %d):
    sum = sum + v[i]
revealed = sum.reveal()
print_ln("Result is %%s", revealed)
resp = Array(1, cint)
resp[0] = revealed
cint.write_to_socket(client_socket_id, resp)`

func main() {
	urls := flag.String("urls", "", "comma separated player base URLs")
	hosts := flag.String("hosts", "", "comma separated Host headers (Knative routing), same order")
	keys := flag.String("keys", "1234567,7654321", "MAC key shares of the players")
	values := flag.String("values", "100,122", "plaintext secrets to sum")
	expect := flag.String("expect", "222", "expected plaintext result")
	flag.Parse()
	sp := testkit.Params()
	var mk []*big.Int
	macKey := new(big.Int)
	for _, k := range strings.Split(*keys, ",") {
		v, _ := new(big.Int).SetString(k, 10)
		mk = append(mk, v)
		macKey.Add(macKey, v)
	}
	macKey.Mod(macKey, sp.Prime)
	n := len(mk)
	vals := strings.Split(*values, ",")
	// params[p] = base64(share_0 || share_1 ...) per player, each share = value word || mac word
	params := make([][]string, n)
	for _, vs := range vals {
		v, _ := new(big.Int).SetString(vs, 10)
		mac := new(big.Int).Mod(new(big.Int).Mul(v, macKey), sp.Prime)
		vsh, msh := split(sp.Prime, v, n), split(sp.Prime, mac, n)
		for p := 0; p < n; p++ {
			vb, err := sp.ToGfp(vsh[p])
			check(err)
			mb, err := sp.ToGfp(msh[p])
			check(err)
			params[p] = append(params[p], base64.StdEncoding.EncodeToString(append(vb, mb...)))
		}
	}
	gid := fmt.Sprintf("%x", mustRand(16))
	jwt := "Bearer " + base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"alice"}`)) + ".sig"
	us, hs := strings.Split(*urls, ","), strings.Split(*hosts, ",")
	var wg sync.WaitGroup
	res := make([]string, n)
	errs := make([]error, n)
	for p := 0; p < n; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			act := map[string]any{"secretParams": params[p], "gameID": gid, "code": fmt.Sprintf(program, len(vals), len(vals)),
				"output": map[string]string{"type": "PlainText"}}
			body, _ := json.Marshal(act)
			req, _ := http.NewRequest("POST", us[p]+"/?compile=true", bytes.NewReader(body))
			req.Host = hs[p]
			req.Header.Set("Authorization", jwt)
			req.Header.Set("Content-Type", "application/json")
			cl := http.Client{Timeout: 10 * time.Minute}
			resp, err := cl.Do(req)
			if err != nil {
				errs[p] = err
				return
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				errs[p] = fmt.Errorf("player %d: HTTP %d: %s", p, resp.StatusCode, b)
				return
			}
			var r struct{ Response []string }
			if err := json.Unmarshal(b, &r); err != nil || len(r.Response) == 0 {
				errs[p] = fmt.Errorf("player %d: bad response %s", p, b)
				return
			}
			dec, _ := base64.StdEncoding.DecodeString(r.Response[0])
			res[p] = string(dec)
		}(p)
	}
	wg.Wait()
	fail := false
	for p := 0; p < n; p++ {
		if errs[p] != nil {
			fmt.Fprintln(os.Stderr, "ERROR:", errs[p])
			fail = true
			continue
		}
		fmt.Printf("player %d result: %q (expected %q)\n", p, res[p], *expect)
		if res[p] != *expect {
			fail = true
		}
	}
	if fail {
		os.Exit(1)
	}
	fmt.Println("MPC OK")
}

func split(p, v *big.Int, n int) []*big.Int {
	out := make([]*big.Int, n)
	sum := new(big.Int)
	for i := 0; i < n-1; i++ {
		r, err := rand.Int(rand.Reader, p)
		check(err)
		out[i] = r
		sum.Add(sum, r)
	}
	out[n-1] = new(big.Int).Mod(new(big.Int).Sub(v, sum), p)
	return out
}

func mustRand(n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	check(err)
	return b
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
