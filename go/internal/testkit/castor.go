// Package testkit generates consistent multi-party SPDZ tuples and uploads them to Castor
// instances (STOMP), used by the end-to-end tests and tools.
package testkit

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/Samrat7745/amphora/go/internal/javaser"
	"github.com/Samrat7745/amphora/go/spdz"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Params are the field parameters of the test deployment.
func Params() *spdz.Utils {
	p, _ := new(big.Int).SetString("198766463529478683931867765928436695041", 10)
	r, _ := new(big.Int).SetString("141515903391459779531506841503331516415", 10)
	ri, _ := new(big.Int).SetString("133854242216446749056083838363708373830", 10)
	return spdz.New(p, r, ri)
}

func randMod(p *big.Int) *big.Int {
	v, err := rand.Int(rand.Reader, p)
	if err != nil {
		panic(err)
	}
	return v
}

// shareOf splits v into n additive shares mod p.
func shareOf(p, v *big.Int, n int) []*big.Int {
	out := make([]*big.Int, n)
	sum := new(big.Int)
	for i := 0; i < n-1; i++ {
		out[i] = randMod(p)
		sum.Add(sum, out[i])
	}
	last := new(big.Int).Sub(v, sum)
	out[n-1] = last.Mod(last, p)
	return out
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// GenInputMasks returns, per party, n raw INPUT_MASK_GFP tuples (value||mac, 32 bytes each).
func GenInputMasks(sp *spdz.Utils, macKeys []*big.Int, n int) [][]byte {
	np := len(macKeys)
	alpha := new(big.Int)
	for _, k := range macKeys {
		alpha.Add(alpha, k)
	}
	out := make([][]byte, np)
	for i := 0; i < n; i++ {
		r := randMod(sp.Prime)
		mac := new(big.Int).Mul(alpha, r)
		mac.Mod(mac, sp.Prime)
		vs, ms := shareOf(sp.Prime, r, np), shareOf(sp.Prime, mac, np)
		for j := 0; j < np; j++ {
			out[j] = append(out[j], must(sp.ToGfp(vs[j]))...)
			out[j] = append(out[j], must(sp.ToGfp(ms[j]))...)
		}
	}
	return out
}

// GenTriples returns, per party, n raw MULTIPLICATION_TRIPLE_GFP tuples (a,b,c with macs).
func GenTriples(sp *spdz.Utils, macKeys []*big.Int, n int) [][]byte {
	np := len(macKeys)
	alpha := new(big.Int)
	for _, k := range macKeys {
		alpha.Add(alpha, k)
	}
	out := make([][]byte, np)
	for i := 0; i < n; i++ {
		a, b := randMod(sp.Prime), randMod(sp.Prime)
		c := new(big.Int).Mul(a, b)
		c.Mod(c, sp.Prime)
		for _, v := range []*big.Int{a, b, c} {
			mac := new(big.Int).Mul(alpha, v)
			mac.Mod(mac, sp.Prime)
			vs, ms := shareOf(sp.Prime, v, np), shareOf(sp.Prime, mac, np)
			for j := 0; j < np; j++ {
				out[j] = append(out[j], must(sp.ToGfp(vs[j]))...)
				out[j] = append(out[j], must(sp.ToGfp(ms[j]))...)
			}
		}
	}
	return out
}

func wsConnect(base string) (*websocket.Conn, error) {
	url := "ws" + strings.TrimPrefix(base, "http") + "/intra-vcp/ws"
	d := websocket.Dialer{Subprotocols: []string{"v12.stomp"}}
	c, _, err := d.Dial(url, http.Header{"Authorization": {"Bearer x"}})
	if err != nil {
		return nil, err
	}
	_ = c.WriteMessage(websocket.TextMessage, []byte("CONNECT\naccept-version:1.2\nheart-beat:10000,0\n\n\x00"))
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, d2, err := c.ReadMessage()
	if err != nil || !bytes.HasPrefix(d2, []byte("CONNECTED")) {
		c.Close()
		return nil, fmt.Errorf("stomp connect: %q %v", d2, err)
	}
	_ = c.WriteMessage(websocket.TextMessage, []byte("SUBSCRIBE\nid:sub-0\ndestination:/queue/response\n\n\x00"))
	time.Sleep(300 * time.Millisecond)
	return c, nil
}

// UploadAndActivate uploads one chunk and activates it.
func UploadAndActivate(base, chunk, tupleType string, data []byte) error {
	c, err := wsConnect(base)
	if err != nil {
		return err
	}
	defer c.Close()
	payload := javaser.EncodeTupleChunk(chunk, tupleType, data)
	frame := append([]byte(fmt.Sprintf("SEND\ndestination:/app/tuples/upload\ncontent-type:application/octet-stream\ncontent-length:%d\n\n", len(payload))), payload...)
	frame = append(frame, 0)
	if err := c.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return err
	}
	for {
		_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, m, err := c.ReadMessage()
		if err != nil {
			return fmt.Errorf("no upload response: %w", err)
		}
		i := bytes.Index(m, []byte("\n\n"))
		if !bytes.HasPrefix(m, []byte("MESSAGE")) || i < 0 {
			continue
		}
		r, err := javaser.DecodeUploadResponse(bytes.TrimSuffix(m[i+2:], []byte{0}))
		if err != nil {
			return err
		}
		if !r.Success {
			msg := ""
			if r.ErrorMsg != nil {
				msg = *r.ErrorMsg
			}
			return fmt.Errorf("upload to %s failed: %s", base, msg)
		}
		break
	}
	req, _ := http.NewRequest(http.MethodPut, base+"/intra-vcp/tuple-chunks/activate/"+chunk, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("activate on %s: %d %s", base, resp.StatusCode, b)
	}
	return nil
}

// UploadTuples generates and uploads n tuples of the type. castors holds len(macKeys) entries per
// deployment ("group"); every group receives identical tuple data (index within group = party).
func UploadTuples(sp *spdz.Utils, castors []string, macKeys []*big.Int, tupleType string, n int) error {
	var data [][]byte
	switch tupleType {
	case "INPUT_MASK_GFP":
		data = GenInputMasks(sp, macKeys, n)
	case "MULTIPLICATION_TRIPLE_GFP":
		data = GenTriples(sp, macKeys, n)
	default:
		return fmt.Errorf("unsupported tuple type %s", tupleType)
	}
	chunk := uuid.NewString()
	np := len(macKeys)
	if len(castors)%np != 0 {
		return fmt.Errorf("castor count %d is not a multiple of the party count %d", len(castors), np)
	}
	for g := 0; g < len(castors); g += np {
		for j := 0; j < np; j++ {
			if err := UploadAndActivate(castors[g+j], chunk, tupleType, data[j]); err != nil {
				return err
			}
		}
	}
	return nil
}
