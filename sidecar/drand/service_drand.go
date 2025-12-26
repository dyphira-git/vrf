package drand

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/drand/drand/v2/common"
	"github.com/drand/drand/v2/common/chain"
	"github.com/drand/drand/v2/crypto"
	"github.com/drand/kyber"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	sidecarv1 "github.com/vexxvakan/vrf/api/vexxvakan/sidecar/v1"
	scerror "github.com/vexxvakan/vrf/sidecar/errors"
	sidecarmetrics "github.com/vexxvakan/vrf/sidecar/servers/metrics"
)

var (
	errDrandHTTPEndpointRequired       = errors.New("drand HTTP endpoint must be provided")
	errDrandChainConfigIncomplete      = errors.New("drand chain configuration is incomplete: chain hash, public key, period, and genesis are required")
	errSidecarNilDrandChainInfo        = errors.New("sidecar: nil drand chain info")
	errSidecarDrandChainHashMismatch   = errors.New("sidecar: drand chain hash mismatch")
	errSidecarDrandPublicKeyMismatch   = errors.New("sidecar: drand public key mismatch")
	errSidecarDrandPeriodMismatch      = errors.New("sidecar: drand period mismatch")
	errSidecarDrandGenesisMismatch     = errors.New("sidecar: drand genesis mismatch")
	errDrandReturnedNon200             = errors.New("drand returned non-200")
	errDrandInfoReturnedNon200         = errors.New("drand /info returned non-200")
	errInvalidDrandHTTPEndpointScheme  = errors.New("invalid drand HTTP endpoint scheme")
	errDrandHTTPEndpointMissingHost    = errors.New("drand HTTP endpoint must include host")
	errDrandHTTPEndpointNotLoopback    = errors.New("drand HTTP endpoint must be loopback-only")
	errDrandVerificationNotInitialized = errors.New("drand verification not initialized")
	errNilDrandChainInfo               = errors.New("nil drand chain info")
	errEmptyHexString                  = errors.New("empty hex string")
)

var newHTTPClient = func() *http.Client {
	return &http.Client{Timeout: 5 * time.Second}
}

// DrandService implements Service by talking to a local drand HTTP endpoint
// using only statically configured URLs. It can also be used alongside a
// supervised drand subprocess (see StartDrandProcess).
type DrandService struct {
	cfg     Config
	logger  *zap.Logger
	metrics sidecarmetrics.Metrics

	httpClient *http.Client

	sf       singleflight.Group
	fetchSem chan struct{}

	lastSuccessUnixNano atomic.Int64
	chainInfo           *sidecarv1.QueryInfoResponse

	scheme   *crypto.Scheme
	pubKey   kyber.Point
	cacheTTL time.Duration

	cacheMu      sync.RWMutex
	cachedLatest *sidecarv1.QueryRandomnessResponse
	cachedAt     time.Time
}

// NewDrandService constructs a new DrandService, checking the configured drand
// binary version and validating /info against the configured chain params.
func NewDrandService(
	ctx context.Context,
	cfg Config,
	logger *zap.Logger,
	m sidecarmetrics.Metrics,
) (*DrandService, error) {
	if logger == nil {
		logger = zap.NewNop()
	}

	if m == nil {
		m = sidecarmetrics.NewNop()
	}

	if strings.TrimSpace(cfg.DrandHTTP) == "" {
		return nil, errDrandHTTPEndpointRequired
	}

	if !cfg.DrandAllowNonLoopbackHTTP {
		if err := enforceLoopbackHTTP(cfg.DrandHTTP); err != nil {
			return nil, err
		}
	}

	if len(cfg.ChainHash) == 0 || len(cfg.PublicKey) == 0 || cfg.PeriodSeconds == 0 || cfg.GenesisUnixSec == 0 {
		return nil, errDrandChainConfigIncomplete
	}

	if err := checkDrandBinaryVersion(cfg, logger); err != nil {
		return nil, err
	}

	s := &DrandService{
		cfg: cfg,
		logger: logger.With(
			zap.String("component", "sidecar-drand-service"),
		),
		metrics:    m,
		fetchSem:   make(chan struct{}, 1),
		httpClient: newHTTPClient(),
		cacheTTL:   1 * time.Second,
	}
	if s.httpClient == nil {
		s.httpClient = &http.Client{Timeout: 5 * time.Second}
	}

	info, err := s.fetchChainInfo(ctx)
	if err != nil {
		return nil, err
	}

	infoRes, err := queryInfoResponseFromChainInfo(info)
	if err != nil {
		return nil, err
	}

	if err := ValidateDrandChainInfo(infoRes, cfg); err != nil {
		return nil, err
	}

	s.chainInfo = infoRes

	schemeName := strings.TrimSpace(info.GetSchemeName())
	if schemeName == "" {
		schemeName = crypto.DefaultSchemeID
	}

	s.scheme, err = crypto.SchemeFromName(schemeName)
	if err != nil {
		return nil, fmt.Errorf("loading drand scheme %q: %w", schemeName, err)
	}

	s.pubKey = s.scheme.KeyGroup.Point()
	if err := s.pubKey.UnmarshalBinary(cfg.PublicKey); err != nil {
		return nil, fmt.Errorf("decoding drand public key: %w", err)
	}

	return s, nil
}

// ValidateDrandChainInfo enforces that the discovered drand chain info matches
// the configured expected values (which should match on-chain VrfParams).
func ValidateDrandChainInfo(info *sidecarv1.QueryInfoResponse, cfg Config) error {
	if info == nil {
		return errSidecarNilDrandChainInfo
	}

	if !bytes.Equal(info.ChainHash, cfg.ChainHash) {
		return fmt.Errorf("%w: got %x, expected %x", errSidecarDrandChainHashMismatch, info.ChainHash, cfg.ChainHash)
	}

	if !bytes.Equal(info.PublicKey, cfg.PublicKey) {
		return errSidecarDrandPublicKeyMismatch
	}

	if info.PeriodSeconds != cfg.PeriodSeconds {
		return fmt.Errorf("%w: got %d, expected %d", errSidecarDrandPeriodMismatch, info.PeriodSeconds, cfg.PeriodSeconds)
	}

	if info.GenesisUnixSec != cfg.GenesisUnixSec {
		return fmt.Errorf("%w: got %d, expected %d", errSidecarDrandGenesisMismatch, info.GenesisUnixSec, cfg.GenesisUnixSec)
	}

	return nil
}

// Randomness fetches a beacon for the given round from the configured drand
// HTTP endpoint. A round of zero requests the latest beacon. Fetches are
// serialized so that at most one upstream drand HTTP request is in-flight.
func (s *DrandService) Randomness(
	ctx context.Context,
	round uint64,
) (*sidecarv1.QueryRandomnessResponse, error) {
	if round == 0 {
		now := time.Now()
		if beacon, ok := s.cachedLatestBeacon(now); ok {
			s.observeTimeSinceLastSuccess(now)
			return beacon, nil
		}
	}

	key := fmt.Sprintf("round-%d", round)

	v, err, _ := s.sf.Do(key, func() (any, error) {
		release := s.acquireFetch()
		defer release()
		return s.fetchBeacon(ctx, round)
	})

	s.observeTimeSinceLastSuccess(time.Now())

	if err != nil {
		return nil, err
	}

	beacon, ok := v.(*sidecarv1.QueryRandomnessResponse)
	if !ok {
		return nil, scerror.ErrServiceUnavailable
	}

	return beacon, nil
}

// Info returns the drand chain information discovered from /info. This is
// expected to match the on-chain VrfParams exactly.
func (s *DrandService) Info(ctx context.Context) (*sidecarv1.QueryInfoResponse, error) {
	if s.chainInfo != nil {
		return s.chainInfo, nil
	}

	info, err := s.fetchChainInfo(ctx)
	if err != nil {
		return nil, err
	}

	infoRes, err := queryInfoResponseFromChainInfo(info)
	if err != nil {
		return nil, err
	}

	if err := ValidateDrandChainInfo(infoRes, s.cfg); err != nil {
		return nil, err
	}

	s.chainInfo = infoRes
	return infoRes, nil
}

func (s *DrandService) acquireFetch() func() {
	s.fetchSem <- struct{}{}
	return func() { <-s.fetchSem }
}

func (s *DrandService) cacheLatest(now time.Time, beacon *sidecarv1.QueryRandomnessResponse) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.cachedLatest = cloneRandomnessResponse(beacon)
	s.cachedAt = now
}

func (s *DrandService) cachedLatestBeacon(now time.Time) (*sidecarv1.QueryRandomnessResponse, bool) {
	s.cacheMu.RLock()
	defer s.cacheMu.RUnlock()

	if s.cachedLatest == nil {
		return nil, false
	}
	if s.cacheTTL <= 0 || now.Sub(s.cachedAt) > s.cacheTTL {
		return nil, false
	}
	return cloneRandomnessResponse(s.cachedLatest), true
}

func (s *DrandService) observeTimeSinceLastSuccess(now time.Time) {
	lastNanos := s.lastSuccessUnixNano.Load()
	if lastNanos == 0 {
		s.metrics.ObserveTimeSinceLastSuccess(0)
		return
	}

	last := time.Unix(0, lastNanos)
	s.metrics.ObserveTimeSinceLastSuccess(now.Sub(last).Seconds())
}

// drandHTTPBeacon is a minimal view of the drand HTTP randomness response.
type drandHTTPBeacon struct {
	Round             uint64 `json:"round"`
	Randomness        string `json:"randomness"`
	Signature         string `json:"signature"`
	PreviousSignature string `json:"previous_signature"`
}

func (s *DrandService) fetchBeacon(ctx context.Context, round uint64) (*sidecarv1.QueryRandomnessResponse, error) {
	chainHashHex := fmt.Sprintf("%x", s.cfg.ChainHash)
	requestedRound := round
	var servedRound uint64
	result := sidecarmetrics.FetchOther
	var retErr error

	defer func() {
		s.metrics.AddDrandFetch(result)

		fields := []zap.Field{
			zap.Uint64("round", requestedRound),
			zap.Uint64("served_round", servedRound),
			zap.String("chain_hash", chainHashHex),
			zap.String("result", string(result)),
		}
		if retErr != nil {
			fields = append(fields, zap.Error(retErr))
		}

		switch result {
		case sidecarmetrics.FetchSuccess, sidecarmetrics.FetchNotFound:
			s.logger.Info("drand fetch attempt", fields...)
		case sidecarmetrics.FetchTimeout,
			sidecarmetrics.FetchHTTPError,
			sidecarmetrics.FetchDecodeError,
			sidecarmetrics.FetchHashMismatch,
			sidecarmetrics.FetchBadSignature,
			sidecarmetrics.FetchWrongRound,
			sidecarmetrics.FetchOther:
			s.logger.Warn("drand fetch attempt", fields...)
		default:
			s.logger.Warn("drand fetch attempt (unknown result)", fields...)
		}
	}()

	path := fmt.Sprintf("/%s/public/latest", chainHashHex)
	if round > 0 {
		path = fmt.Sprintf("/%s/public/%d", chainHashHex, round)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(s.cfg.DrandHTTP, "/")+path,
		nil,
	)
	if err != nil {
		retErr = fmt.Errorf("creating drand request: %w", err)
		result = sidecarmetrics.FetchOther
		return nil, retErr
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		retErr = fmt.Errorf("querying drand: %w", err)

		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			result = sidecarmetrics.FetchTimeout
		} else {
			result = sidecarmetrics.FetchHTTPError
		}

		return nil, retErr
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		retErr = scerror.ErrRoundNotAvailable
		result = sidecarmetrics.FetchNotFound
		return nil, retErr
	}

	if resp.StatusCode != http.StatusOK {
		retErr = fmt.Errorf("%w: %s", errDrandReturnedNon200, resp.Status)
		result = sidecarmetrics.FetchHTTPError
		return nil, retErr
	}

	var hb drandHTTPBeacon
	if err := json.NewDecoder(resp.Body).Decode(&hb); err != nil {
		retErr = fmt.Errorf("decoding drand response: %w", err)
		result = sidecarmetrics.FetchDecodeError
		return nil, retErr
	}
	servedRound = hb.Round

	if round > 0 && hb.Round != round {
		retErr = fmt.Errorf("%w: drand returned round %d for requested round %d", scerror.ErrWrongRound, hb.Round, round)
		result = sidecarmetrics.FetchWrongRound
		return nil, retErr
	}

	sig, err := decodeHexBytes(hb.Signature)
	if err != nil {
		retErr = fmt.Errorf("decoding signature: %w", err)
		result = sidecarmetrics.FetchDecodeError
		return nil, retErr
	}

	var prevSig []byte
	if strings.TrimSpace(hb.PreviousSignature) != "" {
		prevSig, err = decodeHexBytes(hb.PreviousSignature)
		if err != nil {
			retErr = fmt.Errorf("decoding previous signature: %w", err)
			result = sidecarmetrics.FetchDecodeError
			return nil, retErr
		}
	}

	randomness := crypto.RandomnessFromSignature(sig)

	// If the endpoint returned randomness, verify it matches the local derivation.
	if strings.TrimSpace(hb.Randomness) != "" {
		gotRand, err := decodeHexBytes(hb.Randomness)
		if err != nil {
			retErr = fmt.Errorf("decoding randomness: %w", err)
			result = sidecarmetrics.FetchDecodeError
			return nil, retErr
		}
		if !bytes.Equal(gotRand, randomness) {
			retErr = fmt.Errorf("%w: drand randomness mismatch", scerror.ErrHashMismatch)
			result = sidecarmetrics.FetchHashMismatch
			return nil, retErr
		}
	}

	if err := s.verifyBeacon(hb.Round, sig, prevSig); err != nil {
		retErr = err
		if errors.Is(err, scerror.ErrBadSignature) {
			result = sidecarmetrics.FetchBadSignature
		} else {
			result = sidecarmetrics.FetchOther
		}
		return nil, retErr
	}

	result = sidecarmetrics.FetchSuccess
	s.metrics.SetDrandLatestRound(hb.Round)
	s.lastSuccessUnixNano.Store(time.Now().UnixNano())

	out := &sidecarv1.QueryRandomnessResponse{
		DrandRound:        hb.Round,
		Randomness:        randomness,
		Signature:         sig,
		PreviousSignature: prevSig,
	}

	if round == 0 {
		s.cacheLatest(time.Now(), out)
	}

	return out, nil
}

func (s *DrandService) verifyBeacon(round uint64, sig, prevSig []byte) error {
	if s.scheme == nil || s.pubKey == nil {
		return errDrandVerificationNotInitialized
	}

	if strings.HasSuffix(s.scheme.Name, "-chained") && round > 1 && len(prevSig) == 0 {
		return fmt.Errorf("%w: missing previous_signature for chained scheme", scerror.ErrBadSignature)
	}

	b := &common.Beacon{
		PreviousSig: prevSig,
		Round:       round,
		Signature:   sig,
	}

	if err := s.scheme.VerifyBeacon(b, s.pubKey); err != nil {
		return fmt.Errorf("%w: %w", scerror.ErrBadSignature, err)
	}

	return nil
}

func (s *DrandService) fetchChainInfo(ctx context.Context) (*chain.Info, error) {
	chainHashHex := fmt.Sprintf("%x", s.cfg.ChainHash)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(s.cfg.DrandHTTP, "/")+fmt.Sprintf("/%s/info", chainHashHex),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("creating drand /info request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying drand /info: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s", errDrandInfoReturnedNon200, resp.Status)
	}

	info, err := chain.InfoFromJSON(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("decoding drand /info response: %w", err)
	}

	return info, nil
}

func queryInfoResponseFromChainInfo(info *chain.Info) (*sidecarv1.QueryInfoResponse, error) {
	if info == nil {
		return nil, errNilDrandChainInfo
	}

	pubKey, err := info.PublicKey.MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("encoding drand public key: %w", err)
	}

	return &sidecarv1.QueryInfoResponse{
		ChainHash:      info.Hash(),
		PublicKey:      pubKey,
		PeriodSeconds:  uint64(info.Period / time.Second),
		GenesisUnixSec: info.GenesisTime,
	}, nil
}

func cloneRandomnessResponse(beacon *sidecarv1.QueryRandomnessResponse) *sidecarv1.QueryRandomnessResponse {
	if beacon == nil {
		return nil
	}

	out := &sidecarv1.QueryRandomnessResponse{
		DrandRound: beacon.DrandRound,
	}
	if len(beacon.Randomness) > 0 {
		out.Randomness = bytes.Clone(beacon.Randomness)
	}
	if len(beacon.Signature) > 0 {
		out.Signature = bytes.Clone(beacon.Signature)
	}
	if len(beacon.PreviousSignature) > 0 {
		out.PreviousSignature = bytes.Clone(beacon.PreviousSignature)
	}
	return out
}

func decodeHexBytes(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		return nil, errEmptyHexString
	}
	return hex.DecodeString(s)
}

func enforceLoopbackHTTP(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid drand HTTP endpoint: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %q", errInvalidDrandHTTPEndpointScheme, u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return errDrandHTTPEndpointMissingHost
	}

	if strings.EqualFold(host, "localhost") {
		return nil
	}

	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%w: got host %q", errDrandHTTPEndpointNotLoopback, host)
	}

	return nil
}
