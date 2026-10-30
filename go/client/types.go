package client

import (
	"math/big"

	"github.com/google/uuid"
)

// Tag value types.
const (
	TagString = "STRING"
	TagLong   = "LONG"
)

// Tag is a key/value pair attached to a secret.
type Tag struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	ValueType string `json:"valueType,omitempty"`
}

// Metadata describes a secret.
type Metadata struct {
	SecretID string `json:"secretId"`
	Tags     []Tag  `json:"tags"`
}

// MetadataPage is one page of the secret list.
type MetadataPage struct {
	Content       []Metadata `json:"content"`
	Number        int        `json:"number"`
	Size          int        `json:"size"`
	TotalElements int64      `json:"totalElements"`
	TotalPages    int        `json:"totalPages"`
}

// Secret is a plaintext secret: a list of field elements plus tags.
type Secret struct {
	SecretID uuid.UUID
	Tags     []Tag
	Data     []*big.Int
}

// NewSecret creates a secret with a random id (Java Secret.of(tags, data)).
func NewSecret(tags []Tag, data []*big.Int) Secret {
	return Secret{SecretID: uuid.New(), Tags: tags, Data: data}
}

// Filter operators.
const (
	OpEquals      = ":"
	OpLessThan    = "<"
	OpGreaterThan = ">"
)

// TagFilter restricts the secret list to secrets whose tag matches.
type TagFilter struct {
	Key, Operator, Value string
}

// Sort orders the secret list by a tag key.
type Sort struct {
	Property string
	Desc     bool
}

// PageRequest selects a page (and optionally its sort order).
type PageRequest struct {
	Page, Size int
	Sort       *Sort
}

// SecretShare is one party's share of a secret (service-to-service API).
type SecretShare struct {
	SecretID uuid.UUID
	Tags     []Tag
	Data     []byte // value || mac words, 32 bytes per element
}

// FactorPair is one pair exchanged during multiplication.
type FactorPair struct{ A, B *big.Int }

// MultiplicationExchangeObject carries interim values of a multiplication between parties.
type MultiplicationExchangeObject struct {
	OperationID   uuid.UUID
	PlayerID      int
	InterimValues []FactorPair
}
