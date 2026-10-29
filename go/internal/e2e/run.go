// Package e2e drives a full multi-party flow (input masks, masked input upload, secret share
// download with Beaver multiplication) against live Amphora instances and verifies the math.
package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/carbynestack/amphora-go/internal/model"
	"github.com/carbynestack/amphora-go/internal/spdz"
)

// Config describes one flow run.
type Config struct {
	Amphoras []string // index = player id
	MacKeys  []*big.Int
	Sp       *spdz.Utils
	SecretID string
	Words    int
	Seed     int64
	Token    string // Authorization header value
}

// Step is one recorded HTTP exchange.
type Step struct {
	Label  string
	Status int
	Body   string
}

// Result is the transcript.
type Result struct{ Steps []Step }

func do(method, url, token, body string) (int, string, error) {
	var rd io.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return 0, "", err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	c := http.Client{Timeout: 60 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), nil
}

type odoJSON struct {
	SecretShares, RShares, VShares, WShares, UShares []byte
	Data                                             []byte
}

func parseODO(body string) (*odoJSON, error) {
	var raw struct {
		SecretShares []byte `json:"secretShares"`
		RShares      []byte `json:"rShares"`
		VShares      []byte `json:"vShares"`
		WShares      []byte `json:"wShares"`
		UShares      []byte `json:"uShares"`
		Data         []byte `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("parse %q: %w", body, err)
	}
	return &odoJSON{raw.SecretShares, raw.RShares, raw.VShares, raw.WShares, raw.UShares, raw.Data}, nil
}

func (c *Config) sum(words func(j int) []byte, i, width int) (*big.Int, error) {
	t := new(big.Int)
	for j := range c.Amphoras {
		v, err := c.Sp.FromGfp(words(j)[i*width : i*width+spdz.WordWidth])
		if err != nil {
			return nil, err
		}
		t.Add(t, v)
	}
	return t.Mod(t, c.Sp.Prime), nil
}

// checkODO verifies Σw = (Σsecret)(Σr) and Σu = (Σv)(Σr) for every word.
func (c *Config) checkODO(odos []*odoJSON) error {
	n := len(odos[0].SecretShares) / spdz.WordWidth
	for i := 0; i < n; i++ {
		get := func(f func(o *odoJSON) []byte) (*big.Int, error) {
			return c.sum(func(j int) []byte { return f(odos[j]) }, i, spdz.WordWidth)
		}
		s, err := get(func(o *odoJSON) []byte { return o.SecretShares })
		if err != nil {
			return err
		}
		r, _ := get(func(o *odoJSON) []byte { return o.RShares })
		v, _ := get(func(o *odoJSON) []byte { return o.VShares })
		w, _ := get(func(o *odoJSON) []byte { return o.WShares })
		u, _ := get(func(o *odoJSON) []byte { return o.UShares })
		sr := new(big.Int).Mul(s, r)
		sr.Mod(sr, c.Sp.Prime)
		vr := new(big.Int).Mul(v, r)
		vr.Mod(vr, c.Sp.Prime)
		if sr.Cmp(w) != 0 || vr.Cmp(u) != 0 {
			return fmt.Errorf("word %d: multiplication check failed (s*r=%v w=%v, v*r=%v u=%v)", i, sr, w, vr, u)
		}
	}
	return nil
}

func (c *Config) parallel(fn func(j int) (int, string, error)) ([]int, []string, error) {
	n := len(c.Amphoras)
	st, bd, errs := make([]int, n), make([]string, n), make([]error, n)
	var wg sync.WaitGroup
	for j := 0; j < n; j++ {
		wg.Add(1)
		go func(j int) { defer wg.Done(); st[j], bd[j], errs[j] = fn(j) }(j)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return st, bd, e
		}
	}
	return st, bd, nil
}

// Run executes the flow; the transcript is returned even on verification failure.
func Run(c Config) (*Result, error) {
	res := &Result{}
	rec := func(label string, st []int, bd []string) {
		for j := range st {
			res.Steps = append(res.Steps, Step{fmt.Sprintf("%s[p%d]", label, j), st[j], bd[j]})
		}
	}
	id := c.SecretID
	// 1. input masks
	st, bd, err := c.parallel(func(j int) (int, string, error) {
		return do("GET", fmt.Sprintf("%s/input-masks?requestId=%s&count=%d", c.Amphoras[j], id, c.Words), "", "")
	})
	rec("input-masks", st, bd)
	if err != nil {
		return res, err
	}
	odos := make([]*odoJSON, len(bd))
	for j := range bd {
		if st[j] != 200 {
			return res, fmt.Errorf("input-masks p%d: %d %s", j, st[j], bd[j])
		}
		if odos[j], err = parseODO(bd[j]); err != nil {
			return res, err
		}
	}
	if err := c.checkODO(odos); err != nil {
		return res, fmt.Errorf("input mask ODO: %w", err)
	}
	// 2. masked input
	rng := rand.New(rand.NewSource(c.Seed))
	xs := make([]*big.Int, c.Words)
	var data []string
	for i := 0; i < c.Words; i++ {
		x := new(big.Int).Rand(rng, c.Sp.Prime)
		xs[i] = x
		r, err := c.sum(func(j int) []byte { return odos[j].SecretShares }, i, spdz.WordWidth)
		if err != nil {
			return res, err
		}
		m := new(big.Int).Sub(x, r)
		m.Mod(m, c.Sp.Prime)
		mb, err := c.Sp.ToGfp(m)
		if err != nil {
			return res, err
		}
		data = append(data, `{"value":"`+base64.StdEncoding.EncodeToString(mb)+`"}`)
	}
	body := `{"secretId":"` + id + `","data":[`
	for i, d := range data {
		if i > 0 {
			body += ","
		}
		body += d
	}
	body += `],"tags":[{"key":"purpose","value":"e2e"},{"key":"n","value":"42","valueType":"LONG"}]}`
	st, bd, err = c.parallel(func(j int) (int, string, error) {
		return do("POST", c.Amphoras[j]+"/masked-inputs", c.Token, body)
	})
	rec("masked-inputs", st, bd)
	if err != nil {
		return res, err
	}
	for j := range st {
		if st[j] != 201 {
			return res, fmt.Errorf("masked-inputs p%d: %d %s", j, st[j], bd[j])
		}
	}
	// 3. read back with a fresh request id
	rid2 := model.UUIDFromInt(c.Seed + 1)
	st, bd, err = c.parallel(func(j int) (int, string, error) {
		return do("GET", fmt.Sprintf("%s/secret-shares/%s?requestId=%s", c.Amphoras[j], id, rid2), c.Token, "")
	})
	rec("secret-shares", st, bd)
	if err != nil {
		return res, err
	}
	vss := make([]*odoJSON, len(bd))
	for j := range bd {
		if st[j] != 200 {
			return res, fmt.Errorf("secret-shares p%d: %d %s", j, st[j], bd[j])
		}
		if vss[j], err = parseODO(bd[j]); err != nil {
			return res, err
		}
	}
	if err := c.checkODO(vss); err != nil {
		return res, fmt.Errorf("secret share ODO: %w", err)
	}
	alpha := new(big.Int)
	for _, k := range c.MacKeys {
		alpha.Add(alpha, k)
	}
	for i := 0; i < c.Words; i++ {
		x, err := c.sum(func(j int) []byte { return vss[j].Data }, i, spdz.ShareWidth)
		if err != nil {
			return res, err
		}
		if x.Cmp(xs[i]) != 0 {
			return res, fmt.Errorf("word %d: reconstructed %v, want %v", i, x, xs[i])
		}
		mac := new(big.Int)
		for j := range c.Amphoras {
			v, err := c.Sp.FromGfp(vss[j].Data[i*spdz.ShareWidth+spdz.WordWidth : (i+1)*spdz.ShareWidth])
			if err != nil {
				return res, err
			}
			mac.Add(mac, v)
		}
		mac.Mod(mac, c.Sp.Prime)
		want := new(big.Int).Mul(alpha, xs[i])
		want.Mod(want, c.Sp.Prime)
		if mac.Cmp(want) != 0 {
			return res, fmt.Errorf("word %d: mac mismatch", i)
		}
		// secretShares of the response equal the value halves of the stored data
		for j := range c.Amphoras {
			if !bytes.Equal(vss[j].SecretShares[i*spdz.WordWidth:(i+1)*spdz.WordWidth], vss[j].Data[i*spdz.ShareWidth:i*spdz.ShareWidth+spdz.WordWidth]) {
				return res, fmt.Errorf("word %d p%d: secretShares differ from data value half", i, j)
			}
		}
	}
	// 4. tags and delete
	st, bd, err = c.parallel(func(j int) (int, string, error) {
		return do("GET", fmt.Sprintf("%s/secret-shares/%s/tags", c.Amphoras[j], id), c.Token, "")
	})
	rec("tags", st, bd)
	if err != nil {
		return res, err
	}
	st, bd, err = c.parallel(func(j int) (int, string, error) {
		return do("DELETE", fmt.Sprintf("%s/secret-shares/%s", c.Amphoras[j], id), c.Token, "")
	})
	rec("delete", st, bd)
	return res, err
}
