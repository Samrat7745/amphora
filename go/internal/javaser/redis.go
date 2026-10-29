package javaser

import (
	"math/big"
)

// Redis value codecs for Amphora: the cached InputMask TupleList (castor-common) and the
// MultiplicationExchangeObject (amphora-common), byte-exact with Java's native serialization.

// Share is one SPDZ share: value and mac (16 bytes each for gfp).
type Share struct{ Value, Mac []byte }

// FactorPair is a pair of (possibly negative) big integers.
type FactorPair struct{ A, B *big.Int }

// ExchangeObject mirrors MultiplicationExchangeObject.
type ExchangeObject struct {
	OperationID string
	PlayerID    int32
	Values      []FactorPair
}

func sid(u uint64) int64 { return int64(u) }

const ent = "io.carbynestack.castor.common.entities."

var (
	descTupleList = &desc{ent + "TupleList", 5616885721125435294, scSerializable,
		[]field{{'L', "field", "L" + "io/carbynestack/castor/common/entities/Field;"}, {'L', "tupleCls", "Ljava/lang/Class;"}}, descArrayList}
	descInputMask = &desc{ent + "InputMask", sid(0xcaf628212fecd789), scSerializable, nil, descArrayBackedTuple}
	// ArrayBackedTuple must be declared after the types it references.
	descField = &desc{ent + "Field", -1418465461566891261, scSerializable,
		[]field{{'I', "elementSize", ""}, {'L', "name", "Ljava/lang/String;"}}, nil}
	descGfp      = &desc{ent + "Field$Gfp", sid(0x315a2ef9cec0281c), scSerializable, nil, descField}
	descShare    = &desc{ent + "Share", -641361730366133653, scSerializable, []field{{'[', "mac", "[B"}, {'[', "value", "[B"}}, nil}
	descShareArr = &desc{"[L" + ent + "Share;", sid(0x287eb02cbafd7065), scSerializable, nil, nil}

	descBigInteger = &desc{"java.math.BigInteger", sid(0x8cfc9f1fa93bfb1d), scSerializable | scWriteMethod,
		[]field{{'I', "bitCount", ""}, {'I', "bitLength", ""}, {'I', "firstNonzeroByteNum", ""}, {'I', "lowestSetBit", ""},
			{'I', "signum", ""}, {'[', "magnitude", "[B"}}, descNumber}
	descFactorPair = &desc{"io.carbynestack.amphora.common.FactorPair", 4112420116163719023, scSerializable,
		[]field{{'L', "a", "Ljava/math/BigInteger;"}, {'L', "b", "Ljava/math/BigInteger;"}}, nil}
	descMEO = &desc{"io.carbynestack.amphora.common.MultiplicationExchangeObject", -2506050297585267011, scSerializable,
		[]field{{'I', "playerId", ""}, {'L', "interimValues", "Ljava/util/List;"}, {'L', "operationId", "Ljava/util/UUID;"}}, nil}
)

var descArrayBackedTuple = &desc{ent + "ArrayBackedTuple", 3454006854456361647, scSerializable,
	[]field{{'L', "field", "L" + "io/carbynestack/castor/common/entities/Field;"}, {'[', "shares", "[L" + "io/carbynestack/castor/common/entities/Share;"}}, nil}

func init() { descInputMask.super = descArrayBackedTuple }

func (w *writer) class(d *desc) {
	w.u8(tcClass)
	w.writeDesc(d)
	w.assign()
}

func (w *writer) bigInt(v *big.Int) {
	w.newObject(descBigInteger)
	w.i32(-1)
	w.i32(-1)
	w.i32(-2)
	w.i32(-2)
	w.i32(int32(v.Sign()))
	w.byteArray(new(big.Int).Abs(v).Bytes())
	w.u8(tcEndBlock)
}

// EncodeInputMaskList serialises TupleList<InputMask<Gfp>,Gfp> with one share per tuple; the list's Field is the
// same instance as the tuples' (what Java writes for locally built lists, see the Java-generated goldens).
func EncodeInputMaskList(shares []Share) []byte { return encodeInputMaskList(shares, false) }

// EncodeInputMaskListFromCastor is the variant Amphora writes in production: the tuples come from Castor's
// deserialised response, so the list's Field is a distinct instance in Java's stream (observed with the real jar).
func EncodeInputMaskListFromCastor(shares []Share) []byte { return encodeInputMaskList(shares, true) }

func encodeInputMaskList(shares []Share, distinctListField bool) []byte {
	w := newWriter()
	w.newObject(descTupleList)
	w.i32(int32(len(shares)))
	w.u8(tcBlockData)
	w.u8(4)
	w.i32(int32(len(shares)))
	var fieldHandle = -1
	for _, s := range shares {
		w.newObject(descInputMask)
		// ArrayBackedTuple.field
		if fieldHandle < 0 {
			w.newObject(descGfp)
			fieldHandle = baseHandle + w.next - 1
			w.i32(16)
			w.str("gfp", false)
		} else {
			w.ref(fieldHandle)
		}
		w.u8(tcArray)
		w.writeDesc(descShareArr)
		w.assign()
		w.i32(1)
		w.newObject(descShare)
		w.byteArray(s.Mac)
		w.byteArray(s.Value)
	}
	w.u8(tcEndBlock)
	if fieldHandle < 0 || distinctListField {
		// empty list, or Field.GFP written as its own instance after the elements
		w.newObject(descGfp)
		w.i32(16)
		w.str("gfp", false)
	} else {
		w.ref(fieldHandle)
	}
	w.class(descInputMask)
	return w.b.Bytes()
}

// DecodeInputMaskList parses a Java-serialised TupleList<InputMask<Gfp>,Gfp>.
func DecodeInputMaskList(b []byte) ([]Share, error) {
	v, err := parse(b)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Object)
	if !ok || o.Class != ent+"TupleList" {
		return nil, ErrFormat
	}
	out := []Share{}
	for _, a := range o.Annots {
		t, ok := a.(*Object)
		if !ok {
			continue
		}
		if t.Class != ent+"InputMask" {
			return nil, ErrFormat
		}
		arr, ok := t.Fields["shares"].([]any)
		if !ok || len(arr) != 1 {
			return nil, ErrFormat
		}
		s, ok := arr[0].(*Object)
		if !ok {
			return nil, ErrFormat
		}
		val, ok1 := s.Fields["value"].([]byte)
		mac, ok2 := s.Fields["mac"].([]byte)
		if !ok1 || !ok2 {
			return nil, ErrFormat
		}
		out = append(out, Share{Value: val, Mac: mac})
	}
	return out, nil
}

// EncodeExchangeObject serialises a MultiplicationExchangeObject.
func EncodeExchangeObject(e ExchangeObject) []byte {
	w := newWriter()
	w.newObject(descMEO)
	w.i32(e.PlayerID)
	if e.Values == nil {
		w.null()
	} else {
		w.newObject(descArrayList)
		w.i32(int32(len(e.Values)))
		w.u8(tcBlockData)
		w.u8(4)
		w.i32(int32(len(e.Values)))
		for _, p := range e.Values {
			w.newObject(descFactorPair)
			w.bigInt(p.A)
			w.bigInt(p.B)
		}
		w.u8(tcEndBlock)
	}
	w.uuid(e.OperationID)
	return w.b.Bytes()
}

func bigFrom(v any) (*big.Int, error) {
	o, ok := v.(*Object)
	if !ok || o.Class != "java.math.BigInteger" {
		return nil, ErrFormat
	}
	sg, ok1 := o.Fields["signum"].(int64)
	mag, ok2 := o.Fields["magnitude"].([]byte)
	if !ok1 || !ok2 {
		return nil, ErrFormat
	}
	r := new(big.Int).SetBytes(mag)
	if sg < 0 {
		r.Neg(r)
	}
	return r, nil
}

// DecodeExchangeObject parses a Java-serialised MultiplicationExchangeObject.
func DecodeExchangeObject(b []byte) (*ExchangeObject, error) {
	v, err := parse(b)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Object)
	if !ok || o.Class != "io.carbynestack.amphora.common.MultiplicationExchangeObject" {
		return nil, ErrFormat
	}
	pid, ok := o.Fields["playerId"].(int64)
	if !ok {
		return nil, ErrFormat
	}
	id, err := uuidFrom(o.Fields["operationId"])
	if err != nil {
		return nil, err
	}
	res := &ExchangeObject{OperationID: id, PlayerID: int32(pid), Values: []FactorPair{}}
	if o.Fields["interimValues"] == nil {
		res.Values = nil
		return res, nil
	}
	l, ok := o.Fields["interimValues"].(*Object)
	if !ok {
		return nil, ErrFormat
	}
	for _, a := range l.Annots {
		fp, ok := a.(*Object)
		if !ok {
			continue
		}
		x, err1 := bigFrom(fp.Fields["a"])
		y, err2 := bigFrom(fp.Fields["b"])
		if err1 != nil || err2 != nil {
			return nil, ErrFormat
		}
		res.Values = append(res.Values, FactorPair{x, y})
	}
	return res, nil
}
