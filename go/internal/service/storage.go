package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/Samrat7745/amphora/go/internal/castor"
	"github.com/Samrat7745/amphora/go/internal/javaser"
	"github.com/Samrat7745/amphora/go/internal/model"
	"github.com/Samrat7745/amphora/go/internal/objstore"
	"github.com/Samrat7745/amphora/go/internal/opa"
	"github.com/Samrat7745/amphora/go/internal/pgstore"
	"github.com/Samrat7745/amphora/go/internal/rediscache"
	"github.com/Samrat7745/amphora/go/spdz"
	"github.com/jackc/pgx/v5/pgconn"
)

// Messages (identical to the Java constants).
const (
	CreationDateKey           = "creation-date"
	TagsWithSameKeyMsg        = "Two or more tags with the same key defined."
	SecretWithIDExistsMsg     = "A secret with the given id already exists."
	IsReservedKeyMsg          = "\"%s\" is a reserved key."
	NoSecretWithIDMsg         = "No secret with the given id #%s exists."
	NoTagWithKeyMsg           = "No tag with key \"%s\" exists for secret with id #%s."
	TagWithKeyExistsMsg       = "A tag with key \"%s\" already exists for secret #%s"
	NoInputMasksMsg           = "No InputMaskList available in Cache for requestId %s"
	TooFewInputMasksMsg       = "Received more input data than available inputMasks."
	GetDataMsg                = "Data could not be retrieved for secret share #%s: %s"
	DeleteDataMsg             = "Error while deleting data for secret share #%s: %s"
	RequestIDMustNotBeNullMsg = "Request identifier must not be null."
	TooLessInputMasksMsg      = "The number of requested Input Masks has to be 1 or greater."
	notAuthorizedRead         = "User is not authorized to read this secret"
	notAuthorizedUse          = "Requesting program is not authorized to access the secret"
	notAuthorizedDelete       = "User is not authorized to delete this secret"
	notAuthorizedTagCreate    = "User is not authorized to create tags for this secret"
	notAuthorizedTagRead      = "User is not authorized to read tags for this secret"
	notAuthorizedTagUpdate    = "User is not authorized to update tags for this secret"
	notAuthorizedTagDelete    = "User is not authorized to delete tags for this secret"
)

// ReservedTagKeys cannot be set by users.
var ReservedTagKeys = []string{CreationDateKey, opa.OwnerTagKey}

func isReserved(key string) bool { return key == CreationDateKey || key == opa.OwnerTagKey }

// Service is the Amphora business logic.
type Service struct {
	PG       *pgstore.Store
	Obj      *objstore.Store
	Cache    *rediscache.Cache
	Opa      *opa.Client
	Castor   TupleSource
	ODO      *ODO
	Spdz     *spdz.Utils
	MacKey   *big.Int
	PlayerID int
	// JavaTagReplaceBug reproduces the Java service where PUT /secret-shares/{id}/tags always fails
	// with a Hibernate ObjectDeletedException (500) because reserved tags are re-saved after deletion.
	JavaTagReplaceBug bool
	Log               *slog.Logger
	Now               func() time.Time // for tests
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func hasDuplicateKey(tags []model.Tag) bool {
	seen := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		if _, dup := seen[t.Key]; dup {
			return true
		}
		seen[t.Key] = struct{}{}
	}
	return false
}

func dropReserved(tags []model.Tag) []model.Tag {
	out := make([]model.Tag, 0, len(tags))
	for _, t := range tags {
		if !isReserved(t.Key) {
			out = append(out, t)
		}
	}
	return out
}

func tagList(rows []pgstore.TagRow) []model.Tag {
	out := make([]model.Tag, len(rows))
	for i, r := range rows {
		out[i] = r.Tag()
	}
	return out
}

// dbErr maps database errors to what Spring/Hibernate surface as exception messages.
func dbErr(err error) error {
	if err == nil {
		return nil
	}
	var me *model.Error
	if errors.As(err, &me) {
		return err
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch {
		case strings.HasPrefix(pe.Code, "22"):
			return model.Internal("could not execute statement; SQL [n/a]; nested exception is org.hibernate.exception.DataException: could not execute statement")
		case strings.HasPrefix(pe.Code, "23"):
			return model.Internal("could not execute statement; SQL [n/a]; constraint [%s]; nested exception is org.hibernate.exception.ConstraintViolationException: could not execute statement", pe.ConstraintName)
		}
	}
	return model.Internal("%s", err.Error())
}

func (s *Service) load(ctx context.Context, id string) ([]pgstore.TagRow, error) {
	ok, rows, err := s.PG.FindSecret(ctx, s.PG.Pool, id)
	if err != nil {
		return nil, dbErr(err)
	}
	if !ok {
		return nil, model.NotFound(NoSecretWithIDMsg, id)
	}
	return rows, nil
}

func (s *Service) authorize(ctx context.Context, subject, action, denied string, rows []pgstore.TagRow) error {
	ok, err := s.Opa.Allowed(ctx, subject, action, tagList(rows))
	if err != nil {
		return err
	}
	if !ok {
		return model.Unauthorized(denied)
	}
	return nil
}

// persist inserts the secret with tags and the data (DB first inside the tx, then Minio, then commit).
func (s *Service) persist(ctx context.Context, share *model.SecretShare, reserved []model.Tag, after func() error) (string, error) {
	tags := dropReserved(share.Tags)
	tags = append(tags, model.Tag{Key: CreationDateKey, Value: strconv.FormatInt(s.now().UnixMilli(), 10), ValueType: model.TypeString})
	tags = append(tags, reserved...)
	if share.Data == nil {
		return "", model.NPE()
	}
	err := s.PG.InTx(ctx, func(q pgstore.Querier) error {
		if err := s.PG.InsertSecret(ctx, q, share.SecretID); err != nil {
			return err
		}
		for _, t := range tags {
			if err := s.PG.InsertTag(ctx, q, share.SecretID, t); err != nil {
				return err
			}
		}
		if err := s.Obj.Put(ctx, share.SecretID, share.Data); err != nil {
			return model.Internal("%s", err.Error())
		}
		if after != nil {
			return after()
		}
		return nil
	})
	if err != nil {
		return "", dbErr(err)
	}
	return share.SecretID, nil
}

// CreateSecret is createSecret(MaskedInput, userId) (E1).
func (s *Service) CreateSecret(ctx context.Context, mi *model.MaskedInput, userID string) (string, error) {
	exists, err := s.PG.Exists(ctx, s.PG.Pool, mi.SecretID)
	if err != nil {
		return "", dbErr(err)
	}
	if exists {
		return "", model.AlreadyExists(SecretWithIDExistsMsg)
	}
	if hasDuplicateKey(mi.Tags) {
		return "", model.BadRequest(TagsWithSameKeyMsg)
	}
	masks, found, err := s.Cache.GetInputMasks(ctx, mi.SecretID)
	if err != nil {
		return "", model.Internal("%s", err.Error())
	}
	if !found {
		return "", model.Internal(NoInputMasksMsg, mi.SecretID)
	}
	data, err := s.convert(mi, masks)
	if err != nil {
		return "", err
	}
	owner, err := model.NewTag(opa.OwnerTagKey, userID, model.TypeString)
	if err != nil {
		return "", model.BadRequest("%s", err.Error())
	}
	share := &model.SecretShare{Metadata: model.Metadata{SecretID: mi.SecretID, Tags: mi.Tags}, Data: data}
	return s.persist(ctx, share, []model.Tag{owner}, func() error {
		if err := s.Cache.DeleteInputMasks(ctx, mi.SecretID); err != nil {
			return model.Internal("%s", err.Error())
		}
		return nil
	})
}

// convert is SecretShareUtil.convertToSecretShare.
func (s *Service) convert(mi *model.MaskedInput, masks []javaser.Share) ([]byte, error) {
	if len(mi.Data) != len(masks) {
		return nil, model.BadRequest(TooFewInputMasksMsg)
	}
	out := make([]byte, 0, len(masks)*spdz.ShareWidth)
	p := s.Spdz.Prime
	zero, _ := s.Spdz.FromGfp(make([]byte, spdz.WordWidth))
	for i, m := range mi.Data {
		if m == nil {
			return nil, model.NPE()
		}
		pub, err := s.Spdz.FromGfp(m)
		if err != nil {
			return nil, model.BadRequest("%s", err.Error())
		}
		indiv := pub
		if s.PlayerID != 0 {
			indiv = zero
		}
		sv, err := s.Spdz.FromGfp(masks[i].Value)
		if err != nil {
			return nil, model.BadRequest("%s", err.Error())
		}
		sm, err := s.Spdz.FromGfp(masks[i].Mac)
		if err != nil {
			return nil, model.BadRequest("%s", err.Error())
		}
		val := new(big.Int).Add(sv, indiv)
		val.Mod(val, p)
		mac := new(big.Int).Mul(s.MacKey, pub)
		mac.Add(mac, sm)
		mac.Mod(mac, p)
		vb, err := s.Spdz.ToGfp(val)
		if err != nil {
			return nil, model.BadRequest("%s", err.Error())
		}
		mb, err := s.Spdz.ToGfp(mac)
		if err != nil {
			return nil, model.BadRequest("%s", err.Error())
		}
		out = append(out, vb...)
		out = append(out, mb...)
	}
	return out, nil
}

// StoreSecretShare is storeSecretShare(SecretShare) (E12).
func (s *Service) StoreSecretShare(ctx context.Context, share *model.SecretShare) (string, error) {
	exists, err := s.PG.Exists(ctx, s.PG.Pool, share.SecretID)
	if err != nil {
		return "", dbErr(err)
	}
	if exists {
		return "", model.AlreadyExists(SecretWithIDExistsMsg)
	}
	if hasDuplicateKey(share.Tags) {
		return "", model.BadRequest(TagsWithSameKeyMsg)
	}
	if share.Tags == nil {
		return "", model.NPE() // getTags().stream() on null
	}
	var reserved []model.Tag
	for _, t := range share.Tags {
		if t.Key == opa.OwnerTagKey {
			reserved = append(reserved, t)
			break
		}
	}
	return s.persist(ctx, share, reserved, nil)
}

// List is getSecretList (E4).
func (s *Service) List(ctx context.Context, p pgstore.ListParams) (model.MetadataPage, error) {
	page, err := s.PG.List(ctx, p)
	return page, dbErr(err)
}

func (s *Service) shareFor(ctx context.Context, id string, rows []pgstore.TagRow) (*model.SecretShare, error) {
	data, err := s.Obj.Get(ctx, id)
	if err != nil {
		return nil, model.Internal(GetDataMsg, id, err.Error())
	}
	return &model.SecretShare{Metadata: model.Metadata{SecretID: id, Tags: tagList(rows)}, Data: data}, nil
}

// UseSecretShare is useSecretShare(secretId, programId) (E13).
func (s *Service) UseSecretShare(ctx context.Context, id, programID string) (*model.SecretShare, error) {
	rows, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, programID, opa.Use, notAuthorizedUse, rows); err != nil {
		return nil, err
	}
	return s.shareFor(ctx, id, rows)
}

// GetSecretShare is getSecretShare(secretId, userId) (E3, before the ODO).
func (s *Service) GetSecretShare(ctx context.Context, id, userID string) (*model.SecretShare, error) {
	rows, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, userID, opa.Read, notAuthorizedRead, rows); err != nil {
		return nil, err
	}
	return s.shareFor(ctx, id, rows)
}

// DeleteSecret is deleteSecret (E5).
func (s *Service) DeleteSecret(ctx context.Context, id, userID string) error {
	rows, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, userID, opa.Delete, notAuthorizedDelete, rows); err != nil {
		return err
	}
	err = s.PG.InTx(ctx, func(q pgstore.Querier) error {
		if err := s.PG.DeleteSecret(ctx, q, id); err != nil {
			return err
		}
		if _, err := s.Obj.Get(ctx, id); err != nil {
			return model.Internal(GetDataMsg, id, err.Error())
		}
		if err := s.Obj.Remove(ctx, id); err != nil {
			return model.Internal(DeleteDataMsg, id, err.Error())
		}
		return nil
	})
	return dbErr(err)
}

// StoreTag is storeTag (E7); returns the key.
func (s *Service) StoreTag(ctx context.Context, id string, tag model.Tag, userID string) (string, error) {
	if isReserved(tag.Key) {
		return "", model.BadRequest(IsReservedKeyMsg, tag.Key)
	}
	rows, err := s.load(ctx, id)
	if err != nil {
		return "", err
	}
	if err := s.authorize(ctx, userID, opa.TagCreate, notAuthorizedTagCreate, rows); err != nil {
		return "", err
	}
	err = s.PG.InTx(ctx, func(q pgstore.Querier) error {
		if err := s.PG.LockSecret(ctx, q, id); err != nil {
			return err
		}
		ex, err := s.PG.FindTag(ctx, q, id, tag.Key)
		if err != nil {
			return err
		}
		if ex != nil {
			return model.AlreadyExists(fmt.Sprintf(TagWithKeyExistsMsg, tag.Key, id))
		}
		return s.PG.InsertTag(ctx, q, id, tag)
	})
	if err != nil {
		return "", dbErr(err)
	}
	return tag.Key, nil
}

// ReplaceTags is replaceTags (E8).
func (s *Service) ReplaceTags(ctx context.Context, id string, tags []model.Tag, userID string) error {
	if hasDuplicateKey(tags) {
		return model.BadRequest(TagsWithSameKeyMsg)
	}
	rows, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, userID, opa.TagUpdate, notAuthorizedTagUpdate, rows); err != nil {
		return err
	}
	if s.JavaTagReplaceBug {
		for _, rk := range ReservedTagKeys {
			t, err := s.PG.FindTag(ctx, s.PG.Pool, id, rk)
			if err != nil {
				return dbErr(err)
			}
			if t != nil {
				return model.Internal("org.hibernate.ObjectDeletedException: deleted instance passed to merge: [io.carbynestack.amphora.service.persistence.metadata.TagEntity#<null>]; nested exception is java.lang.IllegalArgumentException: org.hibernate.ObjectDeletedException: deleted instance passed to merge: [io.carbynestack.amphora.service.persistence.metadata.TagEntity#<null>]")
			}
		}
	}
	return dbErr(s.PG.InTx(ctx, func(q pgstore.Querier) error {
		if err := s.PG.LockSecret(ctx, q, id); err != nil {
			return err
		}
		var keep []int32
		for _, rk := range ReservedTagKeys {
			t, err := s.PG.FindTag(ctx, q, id, rk)
			if err != nil {
				return err
			}
			if t != nil {
				keep = append(keep, t.ID)
			}
		}
		if err := s.PG.DeleteTags(ctx, q, id, keep); err != nil {
			return err
		}
		for _, t := range dropReserved(tags) {
			if err := s.PG.InsertTag(ctx, q, id, t); err != nil {
				return err
			}
		}
		return nil
	}))
}

// RetrieveTags is retrieveTags (E6).
func (s *Service) RetrieveTags(ctx context.Context, id, userID string) ([]model.Tag, error) {
	rows, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.authorize(ctx, userID, opa.TagRead, notAuthorizedTagRead, rows); err != nil {
		return nil, err
	}
	return tagList(rows), nil
}

// RetrieveTag is retrieveTag (E9).
func (s *Service) RetrieveTag(ctx context.Context, id, key, userID string) (model.Tag, error) {
	rows, err := s.load(ctx, id)
	if err != nil {
		return model.Tag{}, err
	}
	if err := s.authorize(ctx, userID, opa.TagRead, notAuthorizedTagRead, rows); err != nil {
		return model.Tag{}, err
	}
	t, err := s.PG.FindTag(ctx, s.PG.Pool, id, key)
	if err != nil {
		return model.Tag{}, dbErr(err)
	}
	if t == nil {
		return model.Tag{}, model.NotFound(NoTagWithKeyMsg, key, id)
	}
	return t.Tag(), nil
}

// UpdateTag is updateTag (E10).
func (s *Service) UpdateTag(ctx context.Context, id string, tag model.Tag, userID string) error {
	if isReserved(tag.Key) {
		return model.BadRequest(IsReservedKeyMsg, tag.Key)
	}
	rows, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, userID, opa.TagUpdate, notAuthorizedTagUpdate, rows); err != nil {
		return err
	}
	return dbErr(s.PG.InTx(ctx, func(q pgstore.Querier) error {
		if err := s.PG.LockSecret(ctx, q, id); err != nil {
			return err
		}
		ex, err := s.PG.FindTag(ctx, q, id, tag.Key)
		if err != nil {
			return err
		}
		if ex == nil {
			return model.NotFound(NoTagWithKeyMsg, tag.Key, id)
		}
		return s.PG.UpdateTag(ctx, q, ex.ID, tag)
	}))
}

// DeleteTag is deleteTag (E11).
func (s *Service) DeleteTag(ctx context.Context, id, key, userID string) error {
	if isReserved(key) {
		return model.BadRequest(IsReservedKeyMsg, key)
	}
	rows, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, userID, opa.TagDelete, notAuthorizedTagDelete, rows); err != nil {
		return err
	}
	return dbErr(s.PG.InTx(ctx, func(q pgstore.Querier) error {
		if err := s.PG.LockSecret(ctx, q, id); err != nil {
			return err
		}
		ex, err := s.PG.FindTag(ctx, q, id, key)
		if err != nil {
			return err
		}
		if ex == nil {
			return model.NotFound(NoTagWithKeyMsg, key, id)
		}
		return s.PG.DeleteTagRow(ctx, q, ex.ID)
	}))
}

// InputMasks is getInputMasksAsOutputDeliveryObject (E2).
func (s *Service) InputMasks(ctx context.Context, requestID string, count int64) (model.OutputDeliveryObject, error) {
	var odo model.OutputDeliveryObject
	masks, err := s.Castor.Download(ctx, requestID, castor.InputMaskGfp, count)
	if err != nil {
		return odo, model.Internal("%s", err.Error())
	}
	if int64(len(masks)) < count {
		return odo, model.Internal("Index %d out of bounds for length %d", len(masks), len(masks))
	}
	data := make([]byte, len(masks)*spdz.WordWidth)
	shares := make([]javaser.Share, len(masks))
	for i, m := range masks {
		if len(m) < 1 {
			return odo, model.Internal("Index 0 out of bounds for length 0")
		}
		if len(m[0].Value) != spdz.WordWidth || len(m[0].Mac) != spdz.WordWidth {
			return odo, model.Internal("share has unexpected size")
		}
		copy(data[i*spdz.WordWidth:], m[0].Value)
		shares[i] = javaser.Share{Value: m[0].Value, Mac: m[0].Mac}
	}
	odoRequestID := spdz.NameUUIDFromBytes([]byte(requestID + "_odo-computation"))
	odo, err = s.ODO.Compute(ctx, data, odoRequestID)
	if err != nil {
		return odo, err
	}
	if err := s.Cache.PutInputMasks(ctx, requestID, shares); err != nil {
		return odo, model.Internal("%s", err.Error())
	}
	return odo, nil
}

// PutInterim stores an interim value set received from a peer (E14).
func (s *Service) PutInterim(ctx context.Context, e model.ExchangeObject) error {
	if err := s.Cache.PutInterim(ctx, e); err != nil {
		return model.Internal("%s", err.Error())
	}
	return nil
}
