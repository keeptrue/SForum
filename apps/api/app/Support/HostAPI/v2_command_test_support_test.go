package hostapi

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	"google.golang.org/protobuf/proto"
)

type fakeProtocolV2CommandBackend struct {
	mu               sync.Mutex
	values           map[string]string
	receipts         map[string]protocolV2CommandReceipt
	audits           []protocolV2CommandAudit
	begins           int
	commits          int
	rollbacks        int
	commitErr        error
	auditErr         error
	saveErr          error
	actorErr         error
	actorCalls       int
	actorPermissions []string
	actorReceipt     bool
	actorFingerprint string
}

func newFakeProtocolV2CommandBackend() *fakeProtocolV2CommandBackend {
	return &fakeProtocolV2CommandBackend{values: map[string]string{}, receipts: map[string]protocolV2CommandReceipt{}}
}

func (b *fakeProtocolV2CommandBackend) Begin(context.Context) (pgx.Tx, error) {
	b.mu.Lock()
	b.begins++
	b.mu.Unlock()
	return &fakeProtocolV2CommandTx{
		backend: b, values: map[string]string{}, receipts: map[string]protocolV2CommandReceipt{},
	}, nil
}

func (b *fakeProtocolV2CommandBackend) ResolveScope(_ context.Context, _ pgx.Tx, scope protocolV2CommandScope) (protocolV2CommandScope, error) {
	return scope, nil
}

func (b *fakeProtocolV2CommandBackend) LockIdempotency(_ context.Context, _ pgx.Tx, scope protocolV2CommandScope) (*protocolV2CommandReceipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	receipt, ok := b.receipts[fakeProtocolV2CommandScopeKey(scope)]
	if !ok {
		return nil, nil
	}
	receipt.Result = proto.Clone(receipt.Result).(*hostv2.CommandResult)
	return &receipt, nil
}

func (b *fakeProtocolV2CommandBackend) AuthorizeActorDelegation(
	_ context.Context,
	_ pgx.Tx,
	_ protocolV2CommandScope,
	delegation protocolV2VerifiedActorDelegation,
	fingerprint string,
	receiptExists bool,
	requiredPermissions []string,
) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.actorCalls++
	b.actorPermissions = append([]string(nil), requiredPermissions...)
	b.actorReceipt = receiptExists
	b.actorFingerprint = fingerprint
	if b.actorErr != nil {
		return 0, b.actorErr
	}
	return delegation.ActorUserID, nil
}

func (b *fakeProtocolV2CommandBackend) SaveResult(_ context.Context, tx pgx.Tx, scope protocolV2CommandScope, receipt protocolV2CommandReceipt) error {
	if b.saveErr != nil {
		return b.saveErr
	}
	fake := tx.(*fakeProtocolV2CommandTx)
	receipt.Result = proto.Clone(receipt.Result).(*hostv2.CommandResult)
	fake.receipts[fakeProtocolV2CommandScopeKey(scope)] = receipt
	return nil
}

func (b *fakeProtocolV2CommandBackend) AppendAudit(_ context.Context, tx pgx.Tx, event protocolV2CommandAudit) (string, error) {
	if b.auditErr != nil {
		return "", b.auditErr
	}
	fake := tx.(*fakeProtocolV2CommandTx)
	fake.audits = append(fake.audits, event)
	return fmt.Sprintf("audit-%d", len(fake.audits)), nil
}

func (b *fakeProtocolV2CommandBackend) beginCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.begins
}

func (b *fakeProtocolV2CommandBackend) commitCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.commits
}

func (b *fakeProtocolV2CommandBackend) rollbackCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rollbacks
}

func (b *fakeProtocolV2CommandBackend) receiptCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.receipts)
}

func (b *fakeProtocolV2CommandBackend) value(key string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.values[key]
}

func (b *fakeProtocolV2CommandBackend) auditSnapshot() []protocolV2CommandAudit {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]protocolV2CommandAudit(nil), b.audits...)
}

func fakeProtocolV2CommandScopeKey(scope protocolV2CommandScope) string {
	return scope.ExtensionID + "\x00" + scope.IdempotencyKey
}

type fakeProtocolV2CommandTx struct {
	backend  *fakeProtocolV2CommandBackend
	values   map[string]string
	receipts map[string]protocolV2CommandReceipt
	audits   []protocolV2CommandAudit
	closed   bool
}

func (tx *fakeProtocolV2CommandTx) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("nested transaction is not supported by the test backend")
}

func (tx *fakeProtocolV2CommandTx) Commit(context.Context) error {
	if tx.closed {
		return pgx.ErrTxClosed
	}
	tx.backend.mu.Lock()
	defer tx.backend.mu.Unlock()
	if tx.backend.commitErr != nil {
		tx.closed = true
		return tx.backend.commitErr
	}
	for key, value := range tx.values {
		tx.backend.values[key] = value
	}
	for key, receipt := range tx.receipts {
		tx.backend.receipts[key] = receipt
	}
	tx.backend.audits = append(tx.backend.audits, tx.audits...)
	tx.backend.commits++
	tx.closed = true
	return nil
}

func (tx *fakeProtocolV2CommandTx) Rollback(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx.closed {
		return pgx.ErrTxClosed
	}
	tx.backend.mu.Lock()
	tx.backend.rollbacks++
	tx.backend.mu.Unlock()
	tx.closed = true
	return nil
}

func (tx *fakeProtocolV2CommandTx) Exec(_ context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	if tx.closed {
		return pgconn.CommandTag{}, pgx.ErrTxClosed
	}
	if sql != "test.write" || len(arguments) != 2 {
		return pgconn.CommandTag{}, fmt.Errorf("unexpected test statement %q", sql)
	}
	key, keyOK := arguments[0].(string)
	value, valueOK := arguments[1].(string)
	if !keyOK || !valueOK {
		return pgconn.CommandTag{}, errors.New("test.write requires string key and value")
	}
	tx.values[key] = value
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (*fakeProtocolV2CommandTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("unused")
}

func (*fakeProtocolV2CommandTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults { return nil }
func (*fakeProtocolV2CommandTx) LargeObjects() pgx.LargeObjects                         { return pgx.LargeObjects{} }
func (*fakeProtocolV2CommandTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("unused")
}
func (*fakeProtocolV2CommandTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}
func (*fakeProtocolV2CommandTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (*fakeProtocolV2CommandTx) Conn() *pgx.Conn                                  { return nil }
