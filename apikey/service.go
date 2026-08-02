package apikey

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	lastUsedMinInterval = time.Minute
	lastUsedFlushEvery  = 5 * time.Second
	lastUsedQueueSize   = 1024
)

type snapshot struct {
	byPrefix map[string]credential
}

type CreateInput struct {
	Name        string
	Description string
	ExpiresAt   *time.Time
}

type UpdateInput struct {
	Name        string
	Description string
	ExpiresAt   *time.Time
	Version     int64
}

type Service struct {
	repo   *SQLiteRepository
	pepper []byte
	logger *slog.Logger
	now    func() time.Time

	current atomic.Pointer[snapshot]

	mutationMu sync.Mutex
	closed     atomic.Bool
	closeOnce  sync.Once

	usedMu       sync.Mutex
	lastEnqueued map[string]time.Time
	usedCh       chan usageEvent
	stopUsage    chan struct{}
	usageDone    chan struct{}
}

type usageEvent struct {
	id string
	at time.Time
}

func NewService(
	ctx context.Context,
	repo *SQLiteRepository,
	pepper []byte,
	legacyAPIKey string,
	logger *slog.Logger,
) (*Service, error) {
	if repo == nil {
		return nil, fmt.Errorf("%w: repository 不可為 nil", ErrValidation)
	}
	if err := validatePepper(pepper); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		repo:         repo,
		pepper:       append([]byte(nil), pepper...),
		logger:       logger,
		now:          time.Now,
		lastEnqueued: make(map[string]time.Time),
		usedCh:       make(chan usageEvent, lastUsedQueueSize),
		stopUsage:    make(chan struct{}),
		usageDone:    make(chan struct{}),
	}
	check := keyDigest(s.pepper, "stock-mcp-api-key-pepper-check-v1")
	if err := repo.EnsurePepper(ctx, check); err != nil {
		return nil, err
	}

	count, err := repo.Count(ctx)
	if err != nil {
		return nil, err
	}
	if count == 0 && legacyAPIKey != "" {
		if err := s.importLegacy(ctx, legacyAPIKey); err != nil {
			return nil, err
		}
	} else {
		active, err := repo.ListActive(ctx)
		if err != nil {
			return nil, err
		}
		s.publish(active)
	}
	go s.usageWorker()
	return s, nil
}

func (s *Service) List(ctx context.Context) ([]APIKey, error) {
	items, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for i := range items {
		items[i] = effectiveItem(items[i], now)
	}
	return items, nil
}

func (s *Service) Get(ctx context.Context, id string) (APIKey, error) {
	item, err := s.repo.Get(ctx, id)
	if err != nil {
		return APIKey{}, err
	}
	return effectiveItem(item, s.now()), nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (APIKey, string, error) {
	name, description, expiresAt, err := validateMetadata(input.Name, input.Description, input.ExpiresAt, s.now())
	if err != nil {
		return APIKey{}, "", err
	}
	generated, err := generateKey()
	if err != nil {
		return APIKey{}, "", errorsWithoutSecret("無法產生 API key", err)
	}
	now := s.now().UTC()
	rec := keyRecord{
		ID: generated.ID, Name: name, Description: description, MaskedKey: generated.Masked,
		Status: StatusActive, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt, Version: 1,
		Prefix: generated.Prefix, Hash: keyDigest(s.pepper, generated.Full), HashAlgorithm: HashAlgorithm,
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if s.closed.Load() {
		return APIKey{}, "", ErrClosed
	}
	active, err := s.repo.Create(ctx, rec, "created")
	if err != nil {
		return APIKey{}, "", err
	}
	s.publish(active)
	s.audit(rec.APIKey, rec.Prefix, "created")
	return rec.APIKey, generated.Full, nil
}

func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (APIKey, error) {
	name, description, expiresAt, err := validateMetadata(input.Name, input.Description, input.ExpiresAt, s.now())
	if err != nil {
		return APIKey{}, err
	}
	if input.Version < 1 {
		return APIKey{}, fmt.Errorf("%w: version 必須大於 0", ErrValidation)
	}

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if s.closed.Load() {
		return APIKey{}, ErrClosed
	}
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return APIKey{}, err
	}
	now := s.now().UTC()
	active, err := s.repo.UpdateMetadata(ctx, id, input.Version, name, description, expiresAt, now)
	if err != nil {
		return APIKey{}, err
	}
	s.publish(active)
	current.Name = name
	current.Description = description
	current.ExpiresAt = expiresAt
	current.UpdatedAt = now
	current.Version++
	s.audit(current, strings.TrimSuffix(current.MaskedKey, "_••••••••••••"), "metadata_updated")
	return effectiveItem(current, now), nil
}

func (s *Service) Enable(ctx context.Context, id string, version int64) (APIKey, error) {
	return s.setStatus(ctx, id, version, StatusActive)
}

func (s *Service) Disable(ctx context.Context, id string, version int64) (APIKey, error) {
	return s.setStatus(ctx, id, version, StatusDisabled)
}

func (s *Service) setStatus(ctx context.Context, id string, version int64, status Status) (APIKey, error) {
	if version < 1 {
		return APIKey{}, fmt.Errorf("%w: version 必須大於 0", ErrValidation)
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if s.closed.Load() {
		return APIKey{}, ErrClosed
	}
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return APIKey{}, err
	}
	now := s.now().UTC()
	active, err := s.repo.SetStatus(ctx, id, version, status, now)
	if err != nil {
		return APIKey{}, err
	}
	s.publish(active)
	current.Status = status
	current.UpdatedAt = now
	current.RevokedAt = nil
	current.Version++
	s.audit(current, strings.TrimSuffix(current.MaskedKey, "_••••••••••••"), statusAction(status))
	return effectiveItem(current, now), nil
}

func (s *Service) Rotate(ctx context.Context, id string, version int64) (APIKey, string, error) {
	if version < 1 {
		return APIKey{}, "", fmt.Errorf("%w: version 必須大於 0", ErrValidation)
	}
	generated, err := generateKey()
	if err != nil {
		return APIKey{}, "", errorsWithoutSecret("無法產生 API key", err)
	}
	newHash := keyDigest(s.pepper, generated.Full)

	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if s.closed.Load() {
		return APIKey{}, "", ErrClosed
	}
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return APIKey{}, "", err
	}
	now := s.now().UTC()
	active, err := s.repo.Rotate(ctx, id, version, generated.Prefix, newHash, now)
	if err != nil {
		return APIKey{}, "", err
	}
	s.publish(active)
	current.MaskedKey = generated.Masked
	current.Status = StatusActive
	current.RevokedAt = nil
	current.UpdatedAt = now
	current.Version++
	s.audit(current, generated.Prefix, "rotated")
	return effectiveItem(current, now), generated.Full, nil
}

func (s *Service) Delete(ctx context.Context, id string, version int64) error {
	if version < 1 {
		return fmt.Errorf("%w: version 必須大於 0", ErrValidation)
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if s.closed.Load() {
		return ErrClosed
	}
	current, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	active, err := s.repo.Delete(ctx, id, version, now)
	if err != nil {
		return err
	}
	s.publish(active)
	s.audit(current, strings.TrimSuffix(current.MaskedKey, "_••••••••••••"), "deleted")
	return nil
}

// Authenticate 驗證熱路徑只讀取不可變記憶體快照，不執行 SQLite 查詢。
func (s *Service) Authenticate(_ context.Context, fullKey string) (Principal, bool) {
	if fullKey == "" || s.closed.Load() {
		return Principal{}, false
	}
	current := s.current.Load()
	if current == nil {
		return Principal{}, false
	}
	prefix := lookupPrefix(s.pepper, fullKey)
	item, ok := current.byPrefix[prefix]
	if !ok || (item.ExpiresAt != nil && !item.ExpiresAt.After(s.now())) {
		return Principal{}, false
	}
	if !secureDigestEqual(keyDigest(s.pepper, fullKey), item.Hash) {
		return Principal{}, false
	}
	s.markUsed(item.ID)
	return Principal{KeyID: item.ID, Prefix: item.Prefix}, true
}

// Reload 只在啟動或明確維運操作使用。失敗時保留既有快照；驗證不會
// 因為半套資料而錯誤授權。
func (s *Service) Reload(ctx context.Context) error {
	active, err := s.repo.ListActive(ctx)
	if err != nil {
		return err
	}
	s.publish(active)
	return nil
}

func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.stopUsage)
		<-s.usageDone
		s.current.Store(&snapshot{byPrefix: map[string]credential{}})
	})
	return nil
}

func (s *Service) importLegacy(ctx context.Context, fullKey string) error {
	now := s.now().UTC()
	id, err := randomURLSafe(randomIDBytes)
	if err != nil {
		return errorsWithoutSecret("無法建立舊 API key migration ID", err)
	}
	prefix := legacyPrefix(s.pepper, fullKey)
	rec := keyRecord{
		APIKey: APIKey{
			ID: id, Name: "Migrated MCP_API_KEY", Description: "由舊 MCP_API_KEY 自動匯入",
			MaskedKey: maskPrefix(prefix), Status: StatusActive,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		},
		Prefix: prefix, Hash: keyDigest(s.pepper, fullKey), HashAlgorithm: HashAlgorithm,
	}
	active, err := s.repo.Create(ctx, rec, "migration_imported")
	if err != nil {
		return err
	}
	s.publish(active)
	s.audit(rec.APIKey, prefix, "migration_imported")
	return nil
}

func (s *Service) publish(items []credential) {
	next := &snapshot{byPrefix: make(map[string]credential, len(items))}
	for _, item := range items {
		item.Hash = append([]byte(nil), item.Hash...)
		next.byPrefix[item.Prefix] = item
	}
	s.current.Store(next)
}

func (s *Service) markUsed(id string) {
	now := s.now().UTC()
	s.usedMu.Lock()
	if last := s.lastEnqueued[id]; !last.IsZero() && now.Sub(last) < lastUsedMinInterval {
		s.usedMu.Unlock()
		return
	}
	s.lastEnqueued[id] = now
	s.usedMu.Unlock()
	select {
	case s.usedCh <- usageEvent{id: id, at: now}:
	default:
		s.usedMu.Lock()
		delete(s.lastEnqueued, id)
		s.usedMu.Unlock()
	}
}

func (s *Service) usageWorker() {
	defer close(s.usageDone)
	ticker := time.NewTicker(lastUsedFlushEvery)
	defer ticker.Stop()
	pending := make(map[string]time.Time)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		batch := pending
		pending = make(map[string]time.Time)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := s.repo.UpdateLastUsed(ctx, batch)
		cancel()
		if err != nil {
			s.logger.Warn("更新 API key 最後使用時間失敗", "count", len(batch))
		}
	}
	for {
		select {
		case event := <-s.usedCh:
			if previous, ok := pending[event.id]; !ok || event.at.After(previous) {
				pending[event.id] = event.at
			}
		case <-ticker.C:
			flush()
		case <-s.stopUsage:
			for {
				select {
				case event := <-s.usedCh:
					pending[event.id] = event.at
				default:
					flush()
					return
				}
			}
		}
	}
}

func validateMetadata(name, description string, expiresAt *time.Time, now time.Time) (string, string, *time.Time, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" || len([]rune(name)) > 100 {
		return "", "", nil, fmt.Errorf("%w: name 必填且不可超過 100 字", ErrValidation)
	}
	if len([]rune(description)) > 1000 {
		return "", "", nil, fmt.Errorf("%w: description 不可超過 1000 字", ErrValidation)
	}
	if expiresAt != nil {
		t := expiresAt.UTC()
		if !t.After(now) {
			return "", "", nil, fmt.Errorf("%w: expiresAt 必須晚於目前時間", ErrValidation)
		}
		expiresAt = &t
	}
	return name, description, expiresAt, nil
}

func effectiveItem(item APIKey, now time.Time) APIKey {
	if item.Status == StatusActive && item.ExpiresAt != nil && !item.ExpiresAt.After(now) {
		item.Status = StatusExpired
	}
	return item
}

func (s *Service) audit(item APIKey, prefix, action string) {
	s.logger.Info("MCP API key 安全事件",
		"key_id", item.ID,
		"key_name", item.Name,
		"key_prefix", prefix,
		"action", action,
		"result", "success",
	)
}

func errorsWithoutSecret(message string, _ error) error {
	return fmt.Errorf("%s", message)
}
